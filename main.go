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

var (
	currentSettings AppConfig
	fileName        *string
	videoReader     video.Reader
	liveFrame       image.Image
	capturedFrame   image.Image
	mu              sync.Mutex
	videoImage      *canvas.Image

	captureBtn *widget.Button
	okBtn      *widget.Button
	cancelBtn  *widget.Button

	cameraChose *widget.Select
	resChose    *widget.Select
	saveBtn     *widget.Button

	myWindow     fyne.Window
	currentTrack mediadevices.Track
)

type AppConfig struct {
	Camera     string
	Resolution string
}

func cameraMenu() {
	myWindow.SetContent(container.NewVBox(videoImage, captureBtn, okBtn, cancelBtn))
}

func settingsMenu() {
	myWindow.SetContent(container.NewVBox(widget.NewLabel("НАСТРОЙКИ ОБОРУДОВАНИЯ"),

		widget.NewLabel("Камера:"),
		cameraChose,

		widget.NewLabel("Разрешение видео:"),
		resChose,
		saveBtn))
}

func captureBtnPress() {
	mu.Lock()
	if liveFrame == nil {
		mu.Unlock()
		return
	}

	bounds := liveFrame.Bounds()
	rgbaClone := image.NewRGBA(bounds)
	draw.Draw(rgbaClone, bounds, liveFrame, bounds.Min, draw.Src)

	capturedFrame = rgbaClone
	mu.Unlock()
}

func okBtnPress() {
	mu.Lock()
	if capturedFrame == nil {
		mu.Unlock()
		slog.Warn("Сначала нажмите кнопку Захват")
		dialog.ShowInformation("Внимание", "Сначала нажмите кнопку Захват!", myWindow)
		return
	}
	imgToSave := capturedFrame
	mu.Unlock()

	file, err := os.Create(*fileName)
	if err != nil {
		slog.Error("Ошибка создания файла:", "error ", err)
		dialog.ShowInformation("Ошибка", "Ошибка создания файла", myWindow)
		return
	}
	defer file.Close()
	defer currentTrack.Close()

	err = jpeg.Encode(file, imgToSave, nil)
	if err != nil {
		slog.Error("Ошибка кодирования JPEG:", "error ", err)
		dialog.ShowInformation("Ошибка", "Ошибка кодирования JPEG", myWindow)
		return
	}

	myWindow.Close()
}

func cancelBtnPress() {
	os.Exit(-1)
}

func saveConfig(configFileName string, settings AppConfig) {
	cfg := ini.Empty()

	section, err := cfg.NewSection("Media")
	if err != nil {
		slog.Error("Ошибка NewSection", "error ", err)
		os.Exit(-1)
	}
	section.Key("Camera").SetValue(settings.Camera)
	section.Key("Resolution").SetValue(settings.Resolution)
	err = cfg.SaveTo(configFileName)
	if err != nil {
		slog.Error("Ошибка сохранения конфига", "error ", err)
		os.Exit(-1)
	}
}

func loadConfig(filePath string) AppConfig {
	defaultConfig := AppConfig{
		Camera:     "Встроенная камера",
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

func main() {
	const configPath = "settings.ini"
	cameraCtx, cameraCancel := context.WithCancel(context.Background())
	savedSettings := loadConfig(configPath)
	fileName = flag.String("f", "", "Путь к файлу для обработки")
	flag.Parse()

	if *fileName == "" {
		slog.Error("Ошибка: не указан файл")
		os.Exit(-1)
	}

	myApp := app.New()
	myWindow = myApp.NewWindow("Проверка камеры")
	myWindow.Resize(fyne.NewSize(1280, 720))

	width := 640
	height := 480

	_, err := fmt.Sscanf(savedSettings.Resolution, "%dx%d", &width, &height)
	if err != nil {
		slog.Warn("Не удалось распознать разрешение из конфига, использован дефолт", "error", err)
		width = 640
		height = 480
	}

	devices := mediadevices.EnumerateDevices()
	targetDeviceID := ""

	for _, device := range devices {
		if device.Kind == mediadevices.VideoInput && device.Label == savedSettings.Camera {
			targetDeviceID = device.DeviceID
			break
		}
	}

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
		slog.Error("Не удалось открыть камеру:", "error", err)
		os.Exit(-1)
	}

	currentTrack = stream.GetVideoTracks()[0]

	defer currentTrack.Close()

	videoTrack := currentTrack.(*mediadevices.VideoTrack)
	videoReader = videoTrack.NewReader(false)

	videoImage = canvas.NewImageFromImage(nil)
	videoImage.FillMode = canvas.ImageFillContain
	videoImage.SetMinSize(fyne.NewSize(640, 480))

	captureBtn = widget.NewButton("Захват", captureBtnPress)
	okBtn = widget.NewButton("OK", okBtnPress)
	cancelBtn = widget.NewButton("Отмена", cancelBtnPress)

	settingsBtn := fyne.NewMenuItem("Настройки", settingsMenu)
	cameraBtn := fyne.NewMenuItem("Камера", cameraMenu)

	var cameraOptions []string
	for _, device := range devices {
		if device.Kind == mediadevices.VideoInput {
			cameraOptions = append(cameraOptions, device.Label)
		}
	}
	if len(cameraOptions) == 0 {
		cameraOptions = append(cameraOptions, "Встроенная камера")
	}

	cameraChose = widget.NewSelect(cameraOptions, func(selected string) {})
	cameraChose.SetSelected(savedSettings.Camera)
	resOptions := []string{"640x480", "1280x720", "1920x1080"}
	resChose = widget.NewSelect(resOptions, func(selected string) {})
	resChose.SetSelected(savedSettings.Resolution)
	saveBtn = widget.NewButton("Сохранить настройки", func() {
		currentSettings = AppConfig{
			Camera:     cameraChose.Selected,
			Resolution: resChose.Selected,
		}
		saveConfig("settings.ini", currentSettings)
	})

	menu := fyne.NewMenu("Режимы", cameraBtn, settingsBtn)
	mainMenu := fyne.NewMainMenu(menu)
	myWindow.SetMainMenu(mainMenu)
	myWindow.SetOnClosed(func() {
		if currentTrack != nil {
			currentTrack.Close()
		}
	})

	cameraMenu()

	go func(ctx context.Context) {
		for {
			frame, release, err := videoReader.Read()
			if err != nil {
				slog.Error("Ошибка чтения потока камеры:", "error", err)
				if release != nil {
					release()
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}

			mu.Lock()
			liveFrame = frame
			mu.Unlock()

			fyne.Do(func() {
				videoImage.Image = frame
				videoImage.Refresh()
			})

			release()
			time.Sleep(33 * time.Millisecond)
		}
	}(cameraCtx)

	myWindow.SetOnClosed(func() {
		cameraCancel()
		if currentTrack != nil {
			currentTrack.Close()
		}
	})

	myWindow.ShowAndRun()
}
