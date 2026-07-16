package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"os"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"

	_ "github.com/pion/mediadevices/pkg/driver/camera"

	"gopkg.in/ini.v1"
)

// Структура, хранящяя настройки приложения
type AppConfig struct {
	Camera     string
	Resolution string
}

// Основная структура приложения, содержащая все зависимости
type CameraApp struct {
	// Настройки
	configPath    string
	fileName      *string
	savedSettings AppConfig
	currentConfig AppConfig

	// Контекст для управления горутиной
	cameraCtx    context.Context
	cameraCancel context.CancelFunc

	// GUI
	app        fyne.App
	window     fyne.Window
	videoImage *canvas.Image

	// Кнопки
	captureBtn *widget.Button
	okBtn      *widget.Button
	cancelBtn  *widget.Button
	saveBtn    *widget.Button

	// Выпадающие списки
	cameraChose *widget.Select
	resChose    *widget.Select

	// Данные камеры
	videoReader   video.Reader
	liveFrame     image.Image
	capturedFrame image.Image
	currentTrack  mediadevices.Track

	// Синхронизация
	mu sync.Mutex
}

// Конструктор приложения
func NewCameraApp() *CameraApp {
	cameraCtx, cameraCancel := context.WithCancel(context.Background())

	return &CameraApp{
		configPath:   "settings.ini",
		cameraCtx:    cameraCtx,
		cameraCancel: cameraCancel,
		fileName:     flag.String("f", "", "Путь к файлу для обработки"),
	}
}

// Загрузка конфига из файла
func (c *CameraApp) loadConfig(filePath string) AppConfig {
	defaultConfig := AppConfig{
		Camera:     "",
		Resolution: "1280x720",
	}
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return defaultConfig
	}
	cfg, err := ini.Load(filePath)
	if err != nil {
		slog.Warn("Ошибка загрузки конфига: ", "error", err)
		return defaultConfig
	}
	section := cfg.Section("Media")

	return AppConfig{
		Camera:     section.Key("Camera").MustString(defaultConfig.Camera),
		Resolution: section.Key("Resolution").MustString(defaultConfig.Resolution),
	}
}

// Сохранение конфига в файл
func (c *CameraApp) saveConfig(configFileName string, settings AppConfig) {
	cfg := ini.Empty()

	section, err := cfg.NewSection("Media")
	if err != nil {
		slog.Error("Ошибка NewSection", "error", err)
		os.Exit(-1)
	}
	section.Key("Camera").SetValue(settings.Camera)
	section.Key("Resolution").SetValue(settings.Resolution)
	err = cfg.SaveTo(configFileName)
	if err != nil {
		slog.Error("Ошибка сохранения конфига", "error", err)
		os.Exit(-1)
	}
}

// Обработчик нажатия "Захват"
func (c *CameraApp) captureBtnPress() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.liveFrame == nil {
		return
	}
	// deepcopy изображения
	bounds := c.liveFrame.Bounds()
	rgbaClone := image.NewRGBA(bounds)
	draw.Draw(rgbaClone, bounds, c.liveFrame, bounds.Min, draw.Src)

	c.capturedFrame = rgbaClone
}

// Обработчик нажатия "Ок"
func (c *CameraApp) okBtnPress() {
	c.mu.Lock()
	if c.capturedFrame == nil {
		c.mu.Unlock()
		slog.Warn("Сначала нажмите кнопку Захват")
		dialog.ShowInformation("Внимание", "Сначала нажмите кнопку Захват!", c.window)
		return
	}
	imgToSave := c.capturedFrame
	c.mu.Unlock()

	file, err := os.Create(*c.fileName)
	if err != nil {
		slog.Error("Ошибка создания файла:", "error", err)
		dialog.ShowInformation("Ошибка", "Ошибка создания файла", c.window)
		return
	}
	defer file.Close()

	err = jpeg.Encode(file, imgToSave, nil)
	if err != nil {
		slog.Error("Ошибка кодирования JPEG:", "error", err)
		dialog.ShowInformation("Ошибка", "Ошибка кодирования JPEG", c.window)
		return
	}

	c.window.Close()
}

// Обработчик нажатия "Отмена"
func (c *CameraApp) cancelBtnPress() {
	os.Exit(-1)
}

// Меню камеры
func (c *CameraApp) cameraMenu() {
	c.window.SetContent(container.NewVBox(
		c.videoImage,
		c.captureBtn,
		c.okBtn,
		c.cancelBtn,
	))
}

// Меню настроек
func (c *CameraApp) settingsMenu() {
	c.window.SetContent(container.NewVBox(
		widget.NewLabel("НАСТРОЙКИ ОБОРУДОВАНИЯ"),
		widget.NewLabel("Камера:"),
		c.cameraChose,
		widget.NewLabel("Разрешение видео:"),
		c.resChose,
		c.saveBtn,
	))
}

// Сама настройка пользовательского интерфейса
func (c *CameraApp) setupUI() {
	// Создаём приложение
	c.app = app.New()
	c.window = c.app.NewWindow("Проверка камеры")
	c.window.Resize(fyne.NewSize(1280, 720))

	// Создаём виджет для видео
	c.videoImage = canvas.NewImageFromImage(nil)
	c.videoImage.FillMode = canvas.ImageFillContain
	c.videoImage.SetMinSize(fyne.NewSize(640, 480))

	// Создаём кнопки
	c.captureBtn = widget.NewButton("Захват", c.captureBtnPress)
	c.okBtn = widget.NewButton("OK", c.okBtnPress)
	c.cancelBtn = widget.NewButton("Отмена", c.cancelBtnPress)

	// Получаем список устройств
	devices := mediadevices.EnumerateDevices()

	// Подготавливаем список камер
	var cameraOptions []string
	for _, device := range devices {
		if device.Kind == mediadevices.VideoInput {
			cameraOptions = append(cameraOptions, device.Label)
		}
	}
	if len(cameraOptions) == 0 {
		cameraOptions = append(cameraOptions, "")
	}

	// Создаём выпадающие списки
	c.cameraChose = widget.NewSelect(cameraOptions, func(selected string) {})
	c.cameraChose.SetSelected(c.savedSettings.Camera)

	resOptions := []string{"640x480", "1280x720", "1920x1080"}
	c.resChose = widget.NewSelect(resOptions, func(selected string) {})
	c.resChose.SetSelected(c.savedSettings.Resolution)

	// Кнопка сохранения настроек
	c.saveBtn = widget.NewButton("Сохранить настройки", func() {
		c.currentConfig = AppConfig{
			Camera:     c.cameraChose.Selected,
			Resolution: c.resChose.Selected,
		}
		c.saveConfig(c.configPath, c.currentConfig)
	})

	// Создаём меню
	settingsBtn := fyne.NewMenuItem("Настройки", c.settingsMenu)
	cameraBtn := fyne.NewMenuItem("Камера", c.cameraMenu)
	menu := fyne.NewMenu("Режимы", cameraBtn, settingsBtn)
	mainMenu := fyne.NewMainMenu(menu)
	c.window.SetMainMenu(mainMenu)

	// Отображаем меню камеры по умолчанию со старта
	c.cameraMenu()
}

// Инициализация камеры
func (c *CameraApp) initCamera() error {
	width := 640
	height := 480

	_, err := fmt.Sscanf(c.savedSettings.Resolution, "%dx%d", &width, &height)
	if err != nil {
		slog.Warn("Не удалось распознать разрешение из конфига, использован дефолт", "error", err)
	}

	// Ищем камеру по названию из настроек
	devices := mediadevices.EnumerateDevices()
	targetDeviceID := ""

	for _, device := range devices {
		if device.Kind == mediadevices.VideoInput && device.Label == c.savedSettings.Camera {
			targetDeviceID = device.DeviceID
			break
		}
	}

	// Запускаем поток
	stream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(constraint *mediadevices.MediaTrackConstraints) {
			constraint.Width = prop.Int(width)
			constraint.Height = prop.Int(height)

			if targetDeviceID != "" {
				constraint.DeviceID = prop.String(targetDeviceID)
			}
		},
	})
	if err != nil {
		return err
	}

	c.currentTrack = stream.GetVideoTracks()[0]

	// Запускаем трек и ридер
	videoTrack := c.currentTrack.(*mediadevices.VideoTrack)
	c.videoReader = videoTrack.NewReader(false)

	return nil
}

// Горутина для чтения кадров с камеры
func (c *CameraApp) readFrames() {
	for {
		select {
		case <-c.cameraCtx.Done():
			slog.Info("Остановка чтения кадров")
			return
		default:
			frame, release, err := c.videoReader.Read()
			if err != nil {
				slog.Error("Ошибка чтения потока камеры:", "error", err)
				if release != nil {
					release()
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}

			c.mu.Lock()
			c.liveFrame = frame
			c.mu.Unlock()

			fyne.Do(func() {
				c.videoImage.Image = frame
				c.videoImage.Refresh()
			})

			release()
			time.Sleep(33 * time.Millisecond)
		}
	}
}

// Запуск приложения
func (c *CameraApp) Run() error {
	// Загружаем настройки
	c.savedSettings = c.loadConfig(c.configPath)

	// Настраиваем интерфейс
	c.setupUI()

	// Инициализируем камеры
	if err := c.initCamera(); err != nil {
		slog.Error("Не удалось открыть камеру:", "error", err)
		c.app.Quit()
		return err
	}

	// Запускаем горутину чтения кадров
	go c.readFrames()

	// Закрытие ресурсов при выходе
	c.window.SetOnClosed(func() {
		c.cameraCancel()
		if c.currentTrack != nil {
			c.currentTrack.Close()
		}
	})

	// Запуск
	c.window.ShowAndRun()
	return nil
}

func main() {
	// Создаём приложение
	cameraApp := NewCameraApp()

	// Парсим флаги
	flag.Parse()

	// Проверяем указан ли файл
	if *cameraApp.fileName == "" {
		slog.Error("Ошибка: не указан файл")
		os.Exit(-1)
	}

	// Запускаем приложение
	if err := cameraApp.Run(); err != nil {
		slog.Error("Ошибка при запуске приложения", "error", err)
		os.Exit(-1)
	}
}
