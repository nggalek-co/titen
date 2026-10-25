package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updaternative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
	"github.com/oneo-me/onevra/source/shells/sidebar"
	xdraw "golang.org/x/image/draw"
	"titen/internal/app"
	"titen/internal/audio"
	"titen/internal/auth"
	"titen/internal/branding"
	"titen/internal/platform"
	"titen/internal/saas"
	"titen/internal/storage"
	"titen/internal/transcript"
	"titen/internal/transcription"
	"titen/locales"
)

type endpointHealth int

const (
	endpointUnknown endpointHealth = iota
	endpointReady
	endpointAttention
	endpointDown
)

type endpointStatus struct {
	health      endpointHealth
	detailKey   string
	minutesLeft int
}

type audioCheckPhase int

const (
	audioUnchecked audioCheckPhase = iota
	audioChecking
	audioReady
	audioFailed
)

type desktop struct {
	audioCheck                             string
	audioPhase                             audioCheckPhase
	audioCheckGeneration                   uint64
	endpointGeneration                     uint64
	win                                    *mygo.Window
	shell                                  *sidebar.Shell
	tray                                   *mygo.Tray
	trayMenu                               *mygo.Menu
	trayCanRecord                          bool
	trayState                              app.State
	service                                *app.Service
	store                                  *storage.Store
	dir                                    string
	settings                               app.Settings
	page                                   string
	title, search                          string
	endpointInput, deviceCode, deviceToken string
	endpoint                               endpointStatus
	message                                string
	snapshot                               app.Snapshot
	meetings                               []storage.Meeting
	detail                                 []transcript.Segment
	selected                               string
	devices                                []audio.Device
	microphone                             string
	tasks                                  sync.WaitGroup
	liveList                               ui.ListState
	root                                   context.Context
	cancel                                 context.CancelFunc
	busy, quitting                         bool
	microphoneTesting                      bool
	microphoneTestLevel                    float64
	microphoneTestStatus                   string
	microphoneTestCancel                   context.CancelFunc
}

func (d *desktop) async(work func() error) {
	if d.busy {
		return
	}
	d.busy = true
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		err := work()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.message = err.Error()
			} else {
				d.message = locales.T("done")
			}
		})
	}()
}

// tr formats a catalog message for the frame's locale.
func (d *desktop) tr(c *ui.Context, key string) string {
	return locales.Catalog.Format(c.Locale(), key, nil)
}
func (d *desktop) trf(c *ui.Context, key string, args map[string]any) string {
	return locales.Catalog.Format(c.Locale(), key, args)
}
func (d *desktop) t(key string) string { return locales.T(key) }

func (d *desktop) stateLabel(c *ui.Context, s app.State) string {
	return d.tr(c, "state."+string(s))
}

func (d *desktop) captureLabel(c *ui.Context, mode string) string {
	switch mode {
	case "Microphone only":
		return d.tr(c, "captureMic")
	case "System audio only":
		return d.tr(c, "captureSystem")
	}
	return d.tr(c, "captureBoth")
}

func (d *desktop) connectAccount() {
	endpoint := d.settings.ServerEndpoint
	d.async(func() error {
		client := &saas.Client{BaseURL: endpoint}
		code, err := client.BeginDevice(d.root, "Titen desktop")
		if err != nil {
			return err
		}
		if err = mygo.Shell.OpenExternal(code.VerifyURL); err != nil {
			return fmt.Errorf("open sign-in page: %w", err)
		}
		d.win.Update(func() {
			d.deviceCode = code.Code
			d.message = locales.T("approveInBrowser")
		})
		ctx, cancel := context.WithDeadline(d.root, code.ExpiresAt)
		defer cancel()
		token, err := client.WaitDevice(ctx, code)
		if err != nil {
			return err
		}
		key := saas.TokenKey(endpoint)
		if key == "" {
			return errors.New(locales.T("invalidEndpoint"))
		}
		if err = auth.SetSecret(key, token); err != nil {
			return err
		}
		d.win.Update(func() { d.deviceToken = token; d.deviceCode = ""; d.probe() })
		return nil
	})
}
func (d *desktop) canRecord() bool {
	return !d.busy && !d.microphoneTesting && d.audioPhase == audioReady && d.endpoint.health == endpointReady
}

func (d *desktop) resume() {
	if !d.canRecord() || d.snapshot.State != app.Paused {
		d.message = locales.T("checkupRequired")
		return
	}
	d.async(func() error { return d.service.Resume(d.root) })
}

func (d *desktop) start() {
	if !d.canRecord() || d.snapshot.State != app.Idle {
		d.message = locales.T("checkupRequired")
		return
	}
	cfg, title := d.settings, d.title
	key := d.deviceToken
	if key == "" {
		var err error
		key, err = auth.GetSecret(saas.TokenKey(cfg.ServerEndpoint))
		if err != nil || key == "" {
			d.message = locales.T("signInFirst")
			return
		}
	}
	d.async(func() error {
		if strings.TrimSpace(title) == "" {
			title = "Meeting " + time.Now().Format("2006-01-02 15:04")
		}
		return d.service.Start(d.root, cfg, key, title)
	})
}
func (d *desktop) stop() {
	if d.microphoneTesting {
		d.microphoneTestCancel()
		return
	}
	d.async(func() error { return d.service.Stop(d.root) })
}

func (d *desktop) setCaptureInputs(microphone, system bool) {
	if !microphone && !system || d.busy || d.snapshot.State != app.Idle && d.snapshot.State != app.Paused {
		return
	}
	mode := "Both"
	if !microphone {
		mode = "System audio only"
	} else if !system {
		mode = "Microphone only"
	}
	cfg := d.settings
	cfg.CaptureMode = mode
	d.async(func() error {
		if err := d.service.ConfigureCapture(cfg); err != nil {
			return err
		}
		if err := app.SaveSettings(d.dir, cfg); err != nil {
			return err
		}
		d.win.Update(func() { d.settings = cfg; d.checkAudio() })
		return nil
	})
}
func (d *desktop) history() {
	d.page = "History"
	if d.store == nil {
		return
	}
	search := d.search
	d.async(func() error {
		items, err := d.store.Meetings(d.root, search)
		d.win.Update(func() { d.meetings = items })
		return err
	})
}
func (d *desktop) openMeeting(id string) {
	d.async(func() error {
		segments, err := d.store.Segments(d.root, id)
		d.win.Update(func() { d.selected = id; d.detail = segments })
		return err
	})
}
func (d *desktop) refreshDevices() {
	helper := d.settings.HelperPath
	d.async(func() error {
		devices, err := audio.ListDevices(d.root, helper)
		if err != nil {
			slog.Warn("microphone discovery failed", "error", err)
		} else {
			slog.Info("microphones discovered", "count", len(devices))
		}
		d.win.Update(func() {
			d.devices = devices
			for _, device := range devices {
				if device.ID == d.settings.MicrophoneID {
					d.microphone = deviceLabel(device)
				}
			}
		})
		return err
	})
}
func (d *desktop) save() {
	cfg := d.settings
	for _, device := range d.devices {
		if deviceLabel(device) == d.microphone {
			cfg.MicrophoneID = device.ID
		}
	}
	if d.microphone == "System default" {
		cfg.MicrophoneID = ""
	}
	endpoint, err := saas.NormalizeEndpoint(d.endpointInput)
	if err != nil {
		d.message = err.Error()
		return
	}
	cfg.ServerEndpoint = endpoint
	if endpoint != d.settings.ServerEndpoint {
		cfg.ServerEndpointCustom = true
	}
	old := d.settings.ServerEndpoint
	d.async(func() error {
		if err := d.service.ConfigureCapture(cfg); err != nil {
			return err
		}
		if err := app.SaveSettings(d.dir, cfg); err != nil {
			return err
		}
		d.win.Update(func() {
			d.settings = cfg
			d.endpointInput = cfg.ServerEndpoint
			if old != cfg.ServerEndpoint {
				d.deviceToken = ""
			}
			d.applyLanguage()
			d.refreshLocale()
			d.message = locales.T("settingsSaved")
			d.probe()
		})
		return nil
	})
}

// applyLanguage makes MyGo, Onevra and the app's own strings follow the saved
// choice; "system" follows the user's languages again.
func (d *desktop) applyLanguage() {
	mygo.App.SetLocale(locales.Tag(d.settings.Language))
}

// refreshLocale re-applies localized chrome after the language changes.
func (d *desktop) refreshLocale() {
	if d.win != nil {
		d.win.SetTitle(locales.T("appTitle"))
	}
	if d.tray != nil {
		d.trayMenu = d.menu()
		d.syncTrayMenu()
		d.tray.SetMenu(d.trayMenu)
	}
	if d.win != nil {
		d.win.Invalidate()
	}
}

func classifyBudget(b saas.Budget, err error) endpointStatus {
	if err == nil {
		if b.MinutesLeft <= 0 {
			return endpointStatus{health: endpointAttention, detailKey: "outOfCredit"}
		}
		if b.MinutesLeft < 10 {
			return endpointStatus{health: endpointAttention, detailKey: "minimumCredit"}
		}
		return endpointStatus{health: endpointReady, minutesLeft: b.MinutesLeft}
	}
	var api *saas.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case 401, 403:
			return endpointStatus{health: endpointAttention, detailKey: "signInRequired"}
		case 402:
			return endpointStatus{health: endpointAttention, detailKey: "outOfCredit"}
		}
		return endpointStatus{health: endpointDown, detailKey: "serverError"}
	}
	return endpointStatus{health: endpointDown, detailKey: "serverUnreachable"}
}

// probe refreshes the endpoint status pill. Read desktop fields on the UI
// thread; the network call runs in a task goroutine.
func (d *desktop) probe() {
	d.endpointGeneration++
	generation := d.endpointGeneration
	d.endpoint = endpointStatus{health: endpointUnknown}
	d.checkAudio()
	endpoint := d.settings.ServerEndpoint
	token := d.deviceToken
	if token == "" {
		var err error
		token, err = auth.GetSecret(saas.TokenKey(endpoint))
		if err == nil && token != "" {
			d.deviceToken = token
		}
	}
	if token == "" {
		d.endpoint = endpointStatus{health: endpointAttention, detailKey: "notSignedIn"}
		return
	}
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		ctx, cancel := context.WithTimeout(d.root, 10*time.Second)
		defer cancel()
		budget, err := (&saas.Client{BaseURL: endpoint, Token: token}).Budget(ctx)
		status := classifyBudget(budget, err)
		slog.Info("account check completed", "health", status.health, "minutes_left", status.minutesLeft, "error", err)
		d.win.Update(func() {
			if generation == d.endpointGeneration {
				d.endpoint = status
			}
		})
	}()
}

func (d *desktop) requestAudioAccess() {
	if d.audioPhase == audioChecking {
		return
	}
	d.audioCheckGeneration++
	generation := d.audioCheckGeneration
	d.audioPhase = audioChecking
	d.audioCheck = locales.T("checkupRunning")
	cfg := audio.CaptureConfig{HelperPath: d.settings.HelperPath, Mode: d.settings.CaptureMode}
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		ctx, cancel := context.WithTimeout(d.root, time.Minute)
		defer cancel()
		err := audio.RequestPermissions(ctx, cfg)
		slog.Info("audio permission request completed", "mode", cfg.Mode, "error", err)
		d.win.Update(func() {
			if generation != d.audioCheckGeneration {
				return
			}
			if err != nil {
				d.audioPhase = audioFailed
				d.audioCheck = err.Error()
				return
			}
			d.probe()
		})
	}()
}

func (d *desktop) checkAudio() {
	d.audioCheckGeneration++
	generation := d.audioCheckGeneration
	d.audioPhase = audioChecking
	d.audioCheck = locales.T("checkupRunning")
	cfg := audio.CaptureConfig{HelperPath: d.settings.HelperPath, Mode: d.settings.CaptureMode}
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		ctx, cancel := context.WithTimeout(d.root, 10*time.Second)
		defer cancel()
		err := audio.CheckPermissions(ctx, cfg)
		slog.Info("audio startup check completed", "mode", cfg.Mode, "error", err)
		d.win.Update(func() {
			if generation != d.audioCheckGeneration {
				return
			}
			d.audioPhase = audioReady
			d.audioCheck = locales.T("checkupAudioReady")
			if err != nil {
				d.audioPhase = audioFailed
				d.audioCheck = err.Error()
			}
		})
	}()
}

func formatMinutes(min int) string {
	if min >= 60 {
		return fmt.Sprintf("%dh %dm", min/60, min%60)
	}
	return fmt.Sprintf("%d min", min)
}

// titlebar builds the floating header area: the recording state and the
// endpoint status pills.
func (d *desktop) titlebar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Padding(4, 10).Gap(7).Radius(999).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
			dot := t.TextMuted
			label := d.tr(c, "checkingServer")
			switch d.endpoint.health {
			case endpointReady:
				dot = t.Success
				mins := d.endpoint.minutesLeft
				if d.snapshot.MinutesKnown {
					mins = d.snapshot.MinutesLeft
				}
				label = d.trf(c, "readyLeft", map[string]any{"left": formatMinutes(mins)})
			case endpointAttention:
				dot = t.Warning
				label = d.tr(c, d.endpoint.detailKey)
			case endpointDown:
				dot = t.Danger
				label = d.tr(c, d.endpoint.detailKey)
			}
			ui.Box(c).Size(6, 6).Radius(999).Background(dot)
			components.Text(c, label).FontSize(11).FontWeight(600)
		})
		ui.Row(c).Padding(4, 10).Gap(7).Radius(999).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
			dot := t.TextMuted
			if d.snapshot.State == app.Recording {
				dot = t.Danger
			}
			ui.Box(c).Size(6, 6).Radius(999).Background(dot)
			components.Text(c, d.stateLabel(c, d.snapshot.State)).FontSize(11).FontWeight(600)
		})
	})
}

// nav builds the sidebar: app identity and the page items.
func (d *desktop) nav(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(sidebar.HeaderHeight(c), 0, 0, 0).Children(func() {
		ui.Row(c).Padding(2, 16, 12, 16).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(28, 28).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
				components.Icon(c, lucide.Mic()).Size(15, 15)
			})
			ui.Column(c).Gap(1).Children(func() {
				components.Text(c, "Titen").FontSize(15).Bold()
				components.Text(c, d.tr(c, "appSubtitle")).FontSize(10).TextColor(t.TextMuted)
			})
		})
		components.Sidebar(c).Children(func() {
			components.SidebarSection(c, d.tr(c, "navWorkspace"), func() {
				items := []struct {
					key   string
					label string
					icon  *ui.SVG
				}{
					{"Record", d.tr(c, "navRecord"), lucide.Mic()},
					{"History", d.tr(c, "navHistory"), lucide.Clock()},
					{"Settings", d.tr(c, "navSettings"), lucide.Settings()},
				}
				active := d.page
				if active == "" {
					active = "Record"
				}
				for _, item := range items {
					it := item
					if components.SidebarItem(c, it.label, it.icon, active == it.key).Clicked() {
						if it.key == "History" {
							d.history()
						} else {
							d.page = it.key
						}
					}
				}
			})
		})
	})
}

// view builds the main content area: test banner, the active page and the
// footer with status messages.
func (d *desktop) view(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyW, d.win.Hide)
	t := c.Theme()
	ui.Column(c).Fill().Padding(sidebar.HeaderHeight(c), 0, 0, 0).Children(func() {
		if d.microphoneTesting {
			ui.Row(c).Padding(10, 24).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Box(c).Size(8, 8).Radius(999).Background(t.Warning)
				components.Text(c, d.tr(c, "micTestActive")).FontSize(12).FontWeight(600)
				components.Text(c, d.microphoneTestStatus).FontSize(12).TextColor(t.TextMuted).Grow(1).SingleLine()
				if components.Button(c, d.tr(c, "stopTest"), components.ButtonSecondary).Clicked() {
					d.microphoneTestCancel()
				}
			})
		}
		ui.Column(c).Grow(1).MinHeight(0).Padding(20, 24).Gap(16).Children(func() {
			if d.audioPhase != audioReady {
				checkColor := t.Warning
				checkIcon := lucide.LoaderCircle()
				if d.audioPhase == audioFailed {
					checkColor = t.Danger
					checkIcon = lucide.TriangleAlert()
				}
				ui.Column(c).FillWidth().Padding(12).Gap(8).Radius(t.Radius).Border(1, checkColor).Background(checkColor.Alpha(0.08)).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						components.Icon(c, checkIcon).Size(16, 16).TextColor(checkColor)
						components.Text(c, d.tr(c, "checkupTitle")).FontSize(13).FontWeight(600).TextColor(checkColor)
					})
					components.Text(c, d.audioCheck).FontSize(12)
					ui.Row(c).Gap(8).Children(func() {
						components.Button(c, d.tr(c, "checkupAgain"), components.ButtonSecondary).Disabled(d.audioPhase == audioChecking).OnClick(d.probe)
						if runtime.GOOS == "darwin" {
							components.Button(c, d.tr(c, "checkupRequestAccess"), components.ButtonSecondary).Disabled(d.audioPhase == audioChecking).OnClick(d.requestAudioAccess)
							components.Button(c, d.tr(c, "checkupPermissions"), components.ButtonSecondary).OnClick(func() {
								_ = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture")
							})
						}
					})
				})
			}
			switch d.page {
			case "History":
				d.historyView(c)
			case "Settings":
				d.settingsView(c)
			default:
				d.recordView(c)
			}
		})
		ui.Row(c).Padding(10, 24).Gap(12).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				components.Icon(c, lucide.Cloud()).Size(13, 13).TextColor(t.TextMuted)
				components.Text(c, d.tr(c, "localTranscriptSaved")).FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			switch d.snapshot.TranscriptionStatus {
			case transcription.ConnectionConnecting:
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Warning).Children(func() {
					components.Text(c, d.tr(c, "transcriptionConnecting")).FontSize(11).FontWeight(500).TextColor(t.Warning).SingleLine()
				})
			case transcription.ConnectionConnected:
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Success).Children(func() {
					components.Text(c, d.tr(c, "transcriptionConnected")).FontSize(11).FontWeight(500).TextColor(t.Success).SingleLine()
				})
			case transcription.ConnectionReconnecting:
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Warning).Children(func() {
					components.Text(c, d.tr(c, "transcriptionReconnecting")).FontSize(11).FontWeight(500).TextColor(t.Warning).SingleLine()
				})
			}
			if d.snapshot.Error != "" {
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Danger).Children(func() {
					components.Text(c, d.snapshot.Error).FontSize(11).FontWeight(500).TextColor(t.Danger).SingleLine()
				})
			}
			if d.message != "" {
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Border).Children(func() {
					components.Text(c, d.message).FontSize(11).FontWeight(500).SingleLine()
				})
			}
		})
	})
}
func (d *desktop) recordView(c *ui.Context) {
	t := c.Theme()
	s := d.snapshot
	levels := &ui.MeterLevels{Warning: 0.75, Critical: 0.92}
	surfaceCard(c, func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(46, 46).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
					components.Icon(c, lucide.Mic()).Size(20, 20)
				})
				ui.Column(c).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						components.Text(c, d.stateLabel(c, s.State)).FontSize(13).FontWeight(600)
						components.Badge(c, d.captureLabel(c, d.settings.CaptureMode))
					})
					components.Text(c, fmt.Sprintf("%02d:%02d:%02d", int(s.Duration.Hours()), int(s.Duration.Minutes())%60, int(s.Duration.Seconds())%60)).
						Font("monospace").FontFeatures("tnum").FontSize(30).Bold()
				})
			})
			ui.Spacer(c)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if s.State == app.Idle {
					// Set disabled on the parent before constructing the styled button,
					// so Onevra applies its disabled appearance as well as semantics.
					ui.Row(c).Disabled(!d.canRecord()).Children(func() {
						components.Button(c, d.tr(c, "startRecording"), components.ButtonPrimary).
							Label(d.tr(c, "startRecording")).
							OnClick(d.start)
					})
				} else {
					if s.State == app.Recording {
						components.Button(c, d.tr(c, "pause"), components.ButtonSecondary).
							Disabled(d.busy).
							OnClick(func() { d.async(d.service.Pause) })
					}
					if s.State == app.Paused {
						components.Button(c, d.tr(c, "resume"), components.ButtonPrimary).
							Disabled(!d.canRecord()).
							OnClick(d.resume)
					}
					components.Button(c, d.tr(c, "stopRecording"), components.ButtonSecondary).
						Disabled(d.busy || s.State == app.Stopping).
						OnClick(d.stop)
				}
			})
		})
		ui.Divider(c)
		microphone := d.settings.CaptureMode != "System audio only"
		system := d.settings.CaptureMode != "Microphone only"
		ui.Row(c).Gap(12).Wrap().Children(func() {
			ui.Column(c).Grow(2).MinWidth(240).Gap(5).Children(func() {
				components.Text(c, d.tr(c, "sessionTitle")).FontSize(11).FontWeight(600).TextColor(t.TextMuted)
				components.TextBox(c.Key("title"), &d.title, components.TextBoxOptions{}).
					Placeholder(d.tr(c, "meetingTitle")).Disabled(s.State != app.Idle)
			})
		})
		ui.Row(c).Gap(14).Wrap().Children(func() {
			ui.Column(c).Grow(1).MinWidth(220).Padding(10, 14).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					mic := components.Checkbox(c.Key("capture-microphone"), &microphone, "").Label(d.tr(c, "microphone")).Disabled(d.busy || s.State != app.Idle && s.State != app.Paused || microphone && !system)
					if mic.Changed() {
						d.setCaptureInputs(microphone, system)
					}
					components.Icon(c, lucide.Mic()).Size(13, 13).MarginStart(10)
					components.Text(c, d.tr(c, "microphone")).FontSize(12).FontWeight(600).MarginStart(6)
					ui.Spacer(c)
					components.Text(c, fmt.Sprintf("%d%%", int(s.MicrophoneLevel*100+0.5))).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Meter(c, s.MicrophoneLevel, 0, 1, levels).FillWidth().Label(d.tr(c, "microphone"))
			})
			ui.Column(c).Grow(1).MinWidth(220).Padding(10, 14).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					sys := components.Checkbox(c.Key("capture-system"), &system, "").Label(d.tr(c, "systemAudio")).Disabled(d.busy || s.State != app.Idle && s.State != app.Paused || system && !microphone)
					if sys.Changed() {
						d.setCaptureInputs(microphone, system)
					}
					components.Icon(c, lucide.Volume2()).Size(13, 13).MarginStart(10)
					components.Text(c, d.tr(c, "systemAudio")).FontSize(12).FontWeight(600).MarginStart(6)
					ui.Spacer(c)
					components.Text(c, fmt.Sprintf("%d%%", int(s.SystemLevel*100+0.5))).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Meter(c, s.SystemLevel, 0, 1, levels).FillWidth().Label(d.tr(c, "systemAudio"))
			})
		})
	})
	ui.Column(c).Grow(1).MinHeight(0).Padding(18, 20).Gap(12).Radius(t.Radius).Border(1, t.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			components.Text(c, d.tr(c, "liveTranscript")).FontSize(16).Bold()
			ui.Spacer(c)
			components.Badge(c, d.trf(c, "segmentsBadge", map[string]any{"count": len(s.Segments)}))
		})
		if len(s.Segments) == 0 && s.Partial == "" {
			ui.Column(c).Grow(1).Center().Gap(8).Padding(24).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Box(c).Size(36, 36).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
					components.Icon(c, lucide.FileText()).Size(16, 16)
				})
				components.Text(c, d.tr(c, "waitingForSpeech")).FontSize(14).FontWeight(600)
				components.Text(c, d.tr(c, "waitingDesc")).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
			})
		}
		rows := transcriptRows(s.Segments)
		d.liveList.Key = func(i int) any {
			if i < len(rows) {
				return rows[i].ID
			}
			return "partial"
		}
		list := ui.List(c.Key("live"), &d.liveList, len(rows)+1, func(i int) {
			if i < len(rows) {
				ui.Box(c).FillWidth().Padding(3, 0).Children(func() {
					renderSegmentRow(c, rows[i], d.trf(c, "speaker", map[string]any{"who": speakerOf(rows[i])}))
				})
			} else if s.Partial != "" {
				ui.Row(c).FillWidth().Padding(10, 12).Gap(10).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
					components.Badge(c, d.tr(c, "liveBadge"))
					components.Text(c, s.Partial).Grow(1).FontSize(13).Italic().TextColor(t.TextMuted)
				})
			} else {
				ui.Box(c).Height(1)
			}
		}).Grow(1).FillWidth()
		if len(s.Segments) == 0 && s.Partial == "" {
			list.Grow(0).Height(1)
		}
	})
}
func speakerOf(s transcript.Segment) string {
	who := strings.TrimSpace(s.Speaker)
	if who == "" {
		who = "1"
	}
	return who
}

func transcriptRows(segments []transcript.Segment) []transcript.Segment {
	rows := make([]transcript.Segment, 0, len(segments))
	for _, segment := range segments {
		if len(rows) > 0 {
			last := &rows[len(rows)-1]
			gap := segment.StartMS - last.EndMS
			if speakerOf(*last) == speakerOf(segment) && gap <= 20_000 {
				if gap > 2000 && endsSentence(last.Text) {
					last.Text = strings.TrimSpace(last.Text) + "\n\n"
					segment.Text = strings.TrimLeft(segment.Text, " \t")
				}
				last.Text += segment.Text
				last.EndMS = segment.EndMS
				continue
			}
		}
		rows = append(rows, segment)
	}
	for i := range rows {
		rows[i].Text = splitLongParagraphs(strings.TrimSpace(rows[i].Text))
	}
	return rows
}

func endsSentence(text string) bool {
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, "\"'”’)]")
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "…")
}

func splitLongParagraphs(text string) string {
	var parts []string
	for _, part := range strings.Split(text, "\n\n") {
		for len(part) > 900 {
			cut := strings.LastIndexAny(part[:500], ".!?…") + 1
			if cut == 0 {
				cut = strings.IndexAny(part[500:], ".!?…") + 501
				if cut < 500 || cut > 750 {
					cut = strings.LastIndex(part[:500], " ")
				}
			}
			if cut <= 0 || cut >= len(part) {
				break
			}
			parts = append(parts, strings.TrimSpace(part[:cut]))
			part = strings.TrimSpace(part[cut:])
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
}
func (d *desktop) segmentLabel(c *ui.Context, s transcript.Segment) string {
	return fmt.Sprintf("[%02d:%02d] %s  %s", s.StartMS/60000, s.StartMS/1000%60, d.trf(c, "speaker", map[string]any{"who": speakerOf(s)}), s.Text)
}
func (d *desktop) historyView(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Grow(1).MinHeight(0).Gap(16).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(330).Shrink(0).Padding(16).Gap(12).Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				components.Text(c, d.tr(c, "meetings")).FontSize(15).Bold()
				ui.Spacer(c)
				components.Badge(c, fmt.Sprintf("%d", len(d.meetings)))
			})
			ui.Row(c).Gap(6).Children(func() {
				components.TextBox(c, &d.search, components.TextBoxOptions{}).
					Placeholder(d.tr(c, "searchPlaceholder")).Grow(1)
				components.Button(c, d.tr(c, "search"), components.ButtonSecondary).Disabled(d.busy).OnClick(d.history)
			})
			ui.Scroll(c.Key("meetings")).Grow(1).Gap(6).Children(func() {
				if len(d.meetings) == 0 {
					ui.Column(c).Padding(20, 12).Gap(4).Center().Children(func() {
						components.Text(c, d.tr(c, "noMeetings")).FontSize(13).FontWeight(600)
						components.Text(c, d.tr(c, "noMeetingsDesc")).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
					})
				}
				for _, m := range d.meetings {
					selected := d.selected == m.ID
					row := ui.ButtonBase(c.Key(m.ID)).FillWidth().Padding(10, 12).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Start)
					if selected {
						row.Background(t.SurfacePressed)
					} else if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
					row.Label(m.Title).Children(func() {
						ui.Column(c).Grow(1).Gap(4).AlignItems(ui.Start).Children(func() {
							components.Text(c, m.Title).FontSize(13).FontWeight(600).SingleLine()
							meta := m.CreatedAt.Format("Jan 2, 2006 · 15:04")
							if m.CreatedAt.IsZero() {
								meta = d.tr(c, "savedLocally")
							}
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								components.Text(c, meta).FontSize(11).TextColor(t.TextMuted)
								if m.InterruptedAt != nil {
									components.Badge(c, d.tr(c, "interruptedMeeting"))
								}
								if m.DocumentID != "" {
									components.Badge(c, d.tr(c, "synced"))
								}
							})
						})
					}).OnClick(func() { d.openMeeting(m.ID) })
				}
			})
		})
		ui.Column(c).Grow(1).MinWidth(0).Padding(18, 20).Gap(14).Radius(t.Radius).Border(1, t.Border).Children(func() {
			var activeMeeting *storage.Meeting
			for i := range d.meetings {
				if d.meetings[i].ID == d.selected {
					activeMeeting = &d.meetings[i]
					break
				}
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					heading := d.tr(c, "transcriptReader")
					sub := d.tr(c, "selectMeeting")
					if activeMeeting != nil {
						heading = activeMeeting.Title
						sub = d.trf(c, "finalizedSegments", map[string]any{"count": len(d.detail)})
					} else if d.selected != "" {
						sub = d.trf(c, "meetingSegments", map[string]any{"id": d.selected, "count": len(d.detail)})
					}
					components.Text(c, heading).FontSize(16).Bold().SingleLine()
					components.Text(c, sub).FontSize(12).TextColor(t.TextMuted)
				})
				components.Button(c, d.tr(c, "copyTranscript"), components.ButtonSecondary).Disabled(d.selected == "").OnClick(func() {
					var b strings.Builder
					for _, s := range d.detail {
						b.WriteString(d.segmentLabel(c, s) + "\n")
					}
					c.Services().WriteClipboard(b.String())
				})
				components.Button(c, d.tr(c, "openTitenMeeting"), components.ButtonSecondary).Disabled(activeMeeting == nil || activeMeeting.CloudMeetingID == "").OnClick(func() {
					if activeMeeting != nil {
						_ = mygo.Shell.OpenExternal(activeMeeting.CloudEndpoint + "/meetings/" + activeMeeting.CloudMeetingID)
					}
				})
			})
			ui.Divider(c)
			if activeMeeting != nil && activeMeeting.InterruptedAt != nil {
				lastFinalMS := int64(-1)
				for _, segment := range d.detail {
					if segment.EndMS > lastFinalMS {
						lastFinalMS = segment.EndMS
					}
				}
				message := d.tr(c, "interruptedTranscript")
				if lastFinalMS >= 0 {
					stamp := fmt.Sprintf("%02d:%02d", lastFinalMS/60000, lastFinalMS/1000%60)
					message = d.trf(c, "interruptedTranscriptAfter", map[string]any{"time": stamp})
				}
				components.Text(c, message).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Scroll(c.Key("detail")).Grow(1).FillWidth().Gap(8).Children(func() {
				if len(d.detail) == 0 {
					ui.Column(c).Grow(1).Padding(32, 16).Center().Gap(6).Radius(t.Radius).Border(1, t.Border).Children(func() {
						components.Icon(c, lucide.FileText()).Size(20, 20).TextColor(t.TextMuted)
						components.Text(c, d.tr(c, "noSegments")).FontSize(13).FontWeight(600)
						components.Text(c, d.tr(c, "noSegmentsDesc")).FontSize(12).TextColor(t.TextMuted)
					})
				}
				for _, s := range transcriptRows(d.detail) {
					renderSegmentRow(c, s, d.trf(c, "speaker", map[string]any{"who": speakerOf(s)}))
				}
			})

		})
	})
}
func (d *desktop) settingsView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c.Key("settings")).Grow(1).FillWidth().Gap(16).Children(func() {
		ui.Row(c).Padding(12, 16).Gap(12).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
			components.Icon(c, lucide.KeyRound()).Size(16, 16).TextColor(t.TextMuted)
			components.Text(c, d.trf(c, "tokenNote", map[string]any{"store": platform.CredentialStore(runtime.GOOS)})).FontSize(12).FontWeight(500).Grow(1)
			components.Button(c, d.tr(c, "openLogs"), components.ButtonSecondary).OnClick(func() {
				mygo.Shell.ShowItemInFolder(filepath.Join(d.dir, "logs", "titen.log"))
			})
		})
		ui.Row(c).Gap(16).AlignItems(ui.Stretch).Wrap().Children(func() {
			surfaceCard(c, func() {
				cardHeader(c, lucide.KeyRound(), d.tr(c, "accountTitle"), d.tr(c, "accountDesc"))
				ui.Divider(c)
				ui.Column(c).Gap(5).Children(func() {
					components.Text(c, d.tr(c, "serverEndpoint")).FontSize(12).FontWeight(600)
					components.TextBox(c.Key("server-endpoint"), &d.endpointInput, components.TextBoxOptions{}).Placeholder(saas.ProductionEndpoint)
					components.Text(c, d.tr(c, "endpointHint")).FontSize(11).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
					components.Button(c, d.tr(c, "signInBrowser"), components.ButtonSecondary).Disabled(d.busy || d.snapshot.State != app.Idle).OnClick(d.connectAccount)
					status := d.tr(c, "notSignedIn")
					if d.deviceToken != "" {
						status = d.tr(c, "signedIn")
					}
					components.Text(c, status).FontSize(12).TextColor(t.TextMuted)
				})
				if d.deviceCode != "" {
					components.Text(c, d.trf(c, "approveCode", map[string]any{"code": d.deviceCode})).FontSize(13).FontWeight(600)
				}
				components.Button(c, d.tr(c, "openBilling"), components.ButtonSecondary).OnClick(func() { _ = mygo.Shell.OpenExternal(d.settings.ServerEndpoint + "/billing") })
				components.Button(c, d.tr(c, "googleSettings"), components.ButtonSecondary).OnClick(func() { _ = mygo.Shell.OpenExternal(d.settings.ServerEndpoint + "/settings") })
				ui.Column(c).Gap(5).Children(func() {
					components.Text(c, d.tr(c, "language")).FontSize(12).FontWeight(600)
					sel := components.NativeSelect(c, &d.settings.Language, []components.NativeSelectOption[string]{
						{Value: locales.System, Label: d.tr(c, "langSystem")},
						{Value: locales.English, Label: "English"},
						{Value: locales.Indonesian, Label: "Bahasa Indonesia"},
					})
					sel.Trigger.FillWidth().Label(d.tr(c, "language"))
					if sel.Changed() {
						d.applyLanguage()
						d.refreshLocale()
						cfg := d.settings
						d.tasks.Add(1)
						go func() {
							defer d.tasks.Done()
							if err := app.SaveSettings(d.dir, cfg); err != nil {
								d.win.Update(func() { d.message = err.Error() })
							}
						}()
					}
				})
			}).Grow(1).Basis(380).MinWidth(320)

			surfaceCard(c, func() {
				cardHeader(c, lucide.Mic(), d.tr(c, "audioTitle"), d.tr(c, "audioDesc"))
				ui.Divider(c)
				ui.Column(c).Gap(6).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						components.Text(c, d.tr(c, "microphone")).FontSize(12).FontWeight(600)
						ui.Spacer(c)
						components.Button(c, d.tr(c, "refreshMics"), components.ButtonSecondary).Disabled(d.busy || d.microphoneTesting).OnClick(d.refreshDevices)
					})
					options := []components.NativeSelectOption[string]{
						{Value: "System default", Label: d.tr(c, "systemDefault")},
					}
					for _, device := range d.devices {
						options = append(options, components.NativeSelectOption[string]{Value: deviceLabel(device), Label: deviceLabel(device)})
					}
					components.NativeSelect(c.Key("microphone"), &d.microphone, options).Trigger.FillWidth().Label(d.tr(c, "microphone"))
				})
				ui.Column(c).Padding(12).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
						if d.microphoneTesting {
							components.Button(c, d.tr(c, "stopMicTest"), components.ButtonSecondary).OnClick(func() { d.microphoneTestCancel() })
						} else {
							components.Button(c, d.tr(c, "testMic"), components.ButtonSecondary).Disabled(d.busy || d.snapshot.State != app.Idle).OnClick(d.testMicrophone)
						}
						ui.Meter(c, d.microphoneTestLevel, 0, 1, &ui.MeterLevels{Warning: 0.75, Critical: 0.92}).Grow(1).MinWidth(140).Label(d.tr(c, "testMic"))
					})
					if d.microphoneTestStatus != "" {
						components.Text(c, d.microphoneTestStatus).FontSize(12).FontWeight(500)
					}
					components.Text(c, d.tr(c, "micTestNote")).FontSize(11).TextColor(t.TextMuted)
					if runtime.GOOS == "windows" {
						components.Button(c, d.tr(c, "micPrivacySettings"), components.ButtonSecondary).OnClick(func() {
							if err := mygo.Shell.OpenExternal("ms-settings:privacy-microphone"); err != nil {
								d.message = err.Error()
							}
						})
					}
				})
				ui.Column(c).Gap(5).Children(func() {
					components.Text(c, d.tr(c, "audioSources")).FontSize(12).FontWeight(600)
					components.NativeSelect(c.Key("capture-mode"), &d.settings.CaptureMode, []components.NativeSelectOption[string]{
						{Value: "Both", Label: d.tr(c, "captureBoth")},
						{Value: "Microphone only", Label: d.tr(c, "captureMic")},
						{Value: "System audio only", Label: d.tr(c, "captureSystem")},
					}).Trigger.FillWidth().Label(d.tr(c, "audioSources"))
				})
				ui.Column(c).Gap(4).Children(func() {
					components.Checkbox(c.Key("retain-audio"), &d.settings.RetainFullAudio, "").Label(d.tr(c, "retainFullAudio")).Disabled(d.busy || d.snapshot.State != app.Idle && d.snapshot.State != app.Paused)
					components.Text(c, d.tr(c, "retainFullAudioHint")).FontSize(11).TextColor(t.TextMuted)
				})
				ui.Column(c).Gap(5).Children(func() {
					components.Text(c, d.tr(c, "helperExe")).FontSize(12).FontWeight(600)
					components.TextBox(c.Key("helper"), &d.settings.HelperPath, components.TextBoxOptions{}).Label(d.tr(c, "helperExe"))
				})
				components.Text(c, d.tr(c, "headphonesNote")).FontSize(11).TextColor(t.TextMuted)
			}).Grow(1).Basis(380).MinWidth(320)
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Spacer(c)
			components.Button(c, d.tr(c, "saveSettings"), components.ButtonPrimary).Disabled(d.busy || d.microphoneTesting || d.snapshot.State != app.Idle && d.snapshot.State != app.Paused).OnClick(d.save)
		})
	})
}

func (d *desktop) menu() *mygo.Menu {
	items := []*mygo.MenuItem{
		{Label: locales.T("menuOpen"), Click: func(*mygo.MenuItem, *mygo.Window) { d.win.Show(); d.win.Focus() }},
		{ID: "start", Label: locales.T("menuStart"), Click: func(*mygo.MenuItem, *mygo.Window) { d.start() }},
		{ID: "pause", Label: locales.T("menuPause"), Click: func(*mygo.MenuItem, *mygo.Window) { d.async(d.service.Pause) }},
		{ID: "resume", Label: locales.T("menuResume"), Click: func(*mygo.MenuItem, *mygo.Window) { d.resume() }},
		{ID: "stop", Label: locales.T("menuStop"), Click: func(*mygo.MenuItem, *mygo.Window) { d.stop() }},
	}
	if runtime.GOOS == "windows" {
		items = append(items, mygo.Separator(), updater.MenuItem())
	}
	items = append(items, mygo.Separator(), &mygo.MenuItem{Label: locales.T("menuQuit"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }})
	return mygo.NewMenu(items)
}

func (d *desktop) syncTrayMenu() {
	if d.trayMenu == nil {
		return
	}
	d.trayMenu.ItemByID("start").SetEnabled(d.canRecord() && d.snapshot.State == app.Idle)
	d.trayMenu.ItemByID("pause").SetEnabled(!d.busy && d.snapshot.State == app.Recording)
	d.trayMenu.ItemByID("resume").SetEnabled(d.canRecord() && d.snapshot.State == app.Paused)
	d.trayMenu.ItemByID("stop").SetEnabled(!d.busy && (d.snapshot.State == app.Recording || d.snapshot.State == app.Paused))
}

func (d *desktop) ready() {
	d.shell = sidebar.NewShell(sidebar.ShellOptions{
		Title:    locales.T("appTitle"),
		Width:    1000,
		Height:   760,
		MinWidth: 720, MinHeight: 560,
		StateKey:     "main",
		LeftWidth:    220,
		Left:         d.nav,
		Content:      d.view,
		ContentTitle: d.titlebar,
	})
	d.win = d.shell.Window()
	if runtime.GOOS == "windows" {
		if err := d.win.SetIcon(appIcon); err != nil {
			slog.Error("set window icon", "error", err)
		}
	}
	d.win.OnClose(func(e *mygo.CloseEvent) {
		if !d.quitting {
			e.PreventDefault()
			d.win.Hide()
		}
	})
	var err error
	d.trayMenu = d.menu()
	d.syncTrayMenu()
	if runtime.GOOS == "darwin" {
		dockIcon, iconErr := branding.Icon(appIcon)
		if iconErr != nil {
			slog.Error("prepare dock icon", "error", iconErr)
			dockIcon = appIcon
		}
		if err := mygo.App.Dock.SetIcon(dockIcon); err != nil {
			slog.Error("set Dock icon", "error", err)
		}
	}
	d.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(), ToolTip: locales.Tf("tipPrefix", map[string]any{"state": locales.T("state.Ready")}), Menu: d.trayMenu})
	if err != nil {
		d.message = locales.T("menuBarUnavailable") + " " + err.Error()
	}
	d.probe()
	d.refreshDevices()
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		beat := time.NewTicker(5 * time.Minute)
		defer beat.Stop()
		for {
			select {
			case <-d.root.Done():
				return
			case <-beat.C:
				d.win.Update(d.probe)
			}
		}
	}()
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-d.root.Done():
				return
			case <-ticker.C:
				s := d.service.Snapshot()
				d.win.Update(func() {
					d.snapshot = s
					if d.tray != nil {
						if ready := d.canRecord(); ready != d.trayCanRecord || s.State != d.trayState {
							d.trayCanRecord, d.trayState = ready, s.State
							d.syncTrayMenu()
						}
						if d.microphoneTesting {
							d.tray.SetToolTip(locales.T("tipMicTest"))
						} else {
							d.tray.SetToolTip(locales.Tf("tipPrefix", map[string]any{"state": locales.T("state." + string(s.State))}))
						}
					}
				})
			}
		}
	}()
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if d.quitting {
			return
		}
		e.PreventDefault()
		if d.busy {
			d.message = locales.T("waitBeforeQuit")
			d.win.Show()
			return
		}
		d.busy = true
		if d.microphoneTestCancel != nil {
			d.microphoneTestCancel()
		}
		go func() {
			_ = d.service.Stop(context.Background())
			d.cancel()
			d.win.Update(func() { d.quitting = true; mygo.App.Quit() })
		}()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if d.tray != nil {
			d.tray.Destroy()
		}
		d.cancel()
	})
}

//go:embed resources/icon.png
var appIcon []byte

func trayIcon() []byte {
	icon, err := branding.Icon(appIcon)
	if err != nil {
		return appIcon
	}
	src, err := png.Decode(bytes.NewReader(icon))
	if err != nil {
		return appIcon
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	xdraw.ApproxBiLinear.Scale(img, img.Bounds(), src, src.Bounds(), draw.Over, nil)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return appIcon
	}
	return b.Bytes()
}

var version = "dev"

func currentVersion() string {
	if packaged := mygo.App.Version(); packaged != "" {
		return packaged
	}
	return version
}

func defaultServerEndpoint() string {
	if mygo.IsDev() {
		return saas.DefaultEndpoint
	}
	return saas.ProductionEndpoint
}

func main() {
	dir, err := os.UserConfigDir()
	if err != nil {
		slog.Error("config directory", "error", err)
		os.Exit(1)
	}
	dir = filepath.Join(dir, "Titen")
	if err = os.MkdirAll(dir, 0700); err != nil {
		slog.Error("create config directory", "error", err)
		os.Exit(1)
	}
	logDir := filepath.Join(dir, "logs")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		slog.Error("create log directory", "error", err)
	} else {
		logPath := filepath.Join(logDir, "titen.log")
		logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if logErr != nil {
			slog.Error("open log file", "error", logErr)
		} else {
			defer logFile.Close()
			slog.SetDefault(slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, logFile), nil)))
			slog.Info("logging initialized", "path", logPath)
		}
	}
	cfg, err := app.LoadSettingsWithDefaultEndpoint(dir, defaultServerEndpoint())
	if err != nil {
		slog.Error("settings", "error", err)
		os.Exit(1)
	}
	if cfg.HelperPath == "" {
		exe, _ := os.Executable()
		cwd, _ := os.Getwd()
		cfg.HelperPath = platform.HelperPath(runtime.GOOS, exe, cwd)
	}

	store, err := storage.Open(filepath.Join(dir, "titen.sqlite3"))
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.RecoverInterruptedMeetings(ctx); err != nil {
		slog.Error("recover interrupted meetings", "error", err)
		os.Exit(1)
	}
	endpoint := cfg.ServerEndpoint
	token, _ := auth.GetSecret(saas.TokenKey(endpoint))
	d := &desktop{service: app.NewService(store), store: store, dir: dir, settings: cfg, endpointInput: endpoint, deviceToken: token, page: "Record", root: ctx, cancel: cancel, microphone: "System default"}
	d.service.RecordingsDir = filepath.Join(dir, "recordings")
	if token != "" {
		go d.service.ResumeAudioUploads(ctx, &saas.Client{BaseURL: endpoint, Token: token})
	}
	d.liveList.FollowEnd = true
	if cfg.MicrophoneID != "" {
		d.microphone = cfg.MicrophoneID
	}
	d.applyLanguage()
	mygo.App.SetName("Titen")
	if runtime.GOOS == "windows" {
		mygo.Use(updaternative.Plugin)
	}
	slog.Info("starting Titen", "version", currentVersion(), "os", runtime.GOOS, "arch", runtime.GOARCH)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(d.ready)
	err = mygo.App.Run()
	cancel()
	d.tasks.Wait()
	if err != nil {
		slog.Error("desktop", "error", err)
		os.Exit(1)
	}
}

func surfaceCard(c *ui.Context, body func()) ui.Element {
	t := c.Theme()
	return ui.Column(c).Padding(20).Gap(14).Radius(t.Radius).Border(1, t.Border).Children(body)
}

func cardHeader(c *ui.Context, icon *ui.SVG, title, subtitle string) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		if icon != nil {
			ui.Box(c).Size(30, 30).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
				components.Icon(c, icon).Size(15, 15)
			})
		}
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			components.Text(c, title).FontSize(15).FontWeight(600)
			if subtitle != "" {
				components.Text(c, subtitle).FontSize(12).TextColor(t.TextMuted)
			}
		})
	})
}

func renderSegmentRow(c *ui.Context, s transcript.Segment, speaker string) {
	t := c.Theme()
	stamp := fmt.Sprintf("%02d:%02d", s.StartMS/60000, s.StartMS/1000%60)
	ui.Row(c).FillWidth().Padding(10, 12).Gap(12).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Start).Children(func() {
		ui.Box(c).Padding(2, 7).Radius(t.Radius-2).Border(1, t.Border).Shrink(0).Children(func() {
			components.Text(c, stamp).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
		})
		components.Badge(c, speaker)
		components.Text(c, s.Text).Grow(1).FontSize(13).LineHeight(1.45).
			Label(stamp + " " + speaker)
	})
}

func deviceLabel(device audio.Device) string { return device.Name + " [" + device.ID + "]" }

func (d *desktop) testMicrophone() {
	if d.busy || d.microphoneTesting || d.service.Snapshot().State != app.Idle {
		return
	}
	cfg := audio.CaptureConfig{HelperPath: d.settings.HelperPath, MicrophoneID: d.settings.MicrophoneID}
	if d.microphone == "System default" {
		cfg.MicrophoneID = ""
	} else {
		for _, device := range d.devices {
			if deviceLabel(device) == d.microphone {
				cfg.MicrophoneID = device.ID
			}
		}
	}
	ctx, cancel := context.WithTimeout(d.root, 10*time.Second)
	d.microphoneTestCancel = cancel
	d.microphoneTesting = true
	d.microphoneTestLevel = 0
	d.microphoneTestStatus = locales.T("micTestSpeak")
	d.tasks.Add(1)
	go func() {
		defer d.tasks.Done()
		defer cancel()
		result, err := audio.TestMicrophone(ctx, cfg, func(level float64) { d.win.Update(func() { d.microphoneTestLevel = level }) })
		d.win.Update(func() {
			d.microphoneTesting = false
			d.microphoneTestLevel = 0
			d.microphoneTestCancel = nil
			switch {
			case err != nil:
				d.microphoneTestStatus = err.Error()
			case result.Peak < 0.01:
				d.microphoneTestStatus = locales.T("micTestNoSignal")
			default:
				d.microphoneTestStatus = locales.T("micTestSignal")
			}
		})
	}()
}
