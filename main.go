package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"titen/internal/app"
	"titen/internal/audio"
	"titen/internal/auth"
	"titen/internal/storage"
	docsync "titen/internal/sync"
	"titen/internal/transcript"
)

type desktop struct {
	win                            *mygo.Window
	tray                           *mygo.Tray
	service                        *app.Service
	store                          *storage.Store
	dir                            string
	settings                       app.Settings
	page                           string
	title, key, search, documentID string
	message, syncStatus            string
	snapshot                       app.Snapshot
	meetings                       []storage.Meeting
	detail                         []transcript.Segment
	selected                       string
	devices                        []audio.Device
	microphone                     string
	google                         *auth.Google
	googleSecret                   string
	authMu                         sync.RWMutex
	tasks                          sync.WaitGroup
	liveList                       ui.ListState
	docs                           *docsync.GoogleDocs
	root                           context.Context
	cancel                         context.CancelFunc
	busy, quitting                 bool
	microphoneTesting              bool
	microphoneTestLevel            float64
	microphoneTestStatus           string
	microphoneTestCancel           context.CancelFunc
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
				d.message = "Done"
			}
		})
	}()
}
func (d *desktop) currentGoogle() *auth.Google {
	d.authMu.RLock()
	defer d.authMu.RUnlock()
	return d.google
}
func (d *desktop) replaceGoogle(id string) {
	g := &auth.Google{ClientID: id, ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"), ClientSecretLookup: func() (string, error) { return auth.GetSecret("google-client-secret/" + id) }, OpenBrowser: func(url string) error { return exec.Command("open", url).Run() }}
	d.authMu.Lock()
	if d.google != nil && d.google.ClientID == id {
		d.authMu.Unlock()
		return
	}
	d.google = g
	d.authMu.Unlock()
}
func (d *desktop) start() {
	if d.microphoneTesting {
		d.message = "Stop the microphone test before starting a meeting"
		return
	}
	cfg, title, existingDoc := d.settings, d.title, strings.TrimSpace(d.documentID)
	d.async(func() error {
		key, err := auth.GetSecret("soniox")
		if err != nil {
			return fmt.Errorf("Soniox key: %w", err)
		}
		if strings.TrimSpace(title) == "" {
			title = "Meeting " + time.Now().Format("2006-01-02 15:04")
		}
		if existingDoc != "" {
			cfg.AutoSync = false
			if err := d.docs.CheckDocument(d.root, existingDoc); err != nil {
				return fmt.Errorf("check existing Google Doc: %w", err)
			}
		}
		if err = d.service.Start(d.root, cfg, key, title); err != nil {
			return err
		}
		if existingDoc != "" {
			return d.store.SetDocument(d.root, d.service.Snapshot().MeetingID, existingDoc)
		}
		return nil
	})
}
func (d *desktop) stop() {
	if d.microphoneTesting {
		d.microphoneTestCancel()
		return
	}
	d.async(func() error { return d.service.Stop(d.root) })
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
	cfg, key, googleSecret := d.settings, d.key, d.googleSecret
	for _, device := range d.devices {
		if deviceLabel(device) == d.microphone {
			cfg.MicrophoneID = device.ID
		}
	}
	if d.microphone == "System default" {
		cfg.MicrophoneID = ""
	}
	d.async(func() error {
		if key != "" {
			if err := auth.SetSecret("soniox", key); err != nil {
				return err
			}
		}
		if googleSecret != "" {
			if cfg.GoogleClientID == "" {
				return fmt.Errorf("enter the Google client ID before saving its secret")
			}
			if err := auth.SetSecret("google-client-secret/"+cfg.GoogleClientID, googleSecret); err != nil {
				return err
			}
		}
		if err := d.service.ConfigureCapture(cfg); err != nil {
			return err
		}
		if err := app.SaveSettings(d.dir, cfg); err != nil {
			return err
		}
		d.win.Update(func() {
			d.settings = cfg
			d.key = ""
			d.googleSecret = ""
			d.replaceGoogle(cfg.GoogleClientID)
		})
		return nil
	})
}
func (d *desktop) view(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyW, d.win.Hide)
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(14, 24).Gap(16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(32, 32).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
					ui.Icon(c, micIcon).Size(16, 16)
				})
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, "Titen").FontSize(18).Bold()
					ui.Text(c, "Meeting Transcription Studio").FontSize(11).TextColor(t.TextMuted)
				})
			})
			ui.Spacer(c)
			ui.Row(c).Padding(4, 10).Gap(7).Radius(999).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
				dot := t.TextMuted
				if d.snapshot.State == app.Recording {
					dot = t.Danger
				}
				ui.Box(c).Size(6, 6).Radius(999).Background(dot)
				ui.Text(c, string(d.snapshot.State)).FontSize(11).FontWeight(600)
			})
			ui.Row(c).Padding(2).Gap(2).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
				navIcons := map[string]*ui.SVG{"Record": micIcon, "History": historyIcon, "Settings": settingsIcon}
				activePage := d.page
				if activePage == "" {
					activePage = "Record"
				}
				for _, page := range []string{"Record", "History", "Settings"} {
					active := activePage == page
					btn := ui.ButtonBase(c.Key(page)).Padding(6, 13).Gap(6).Radius(t.Radius - 2)
					if active {
						btn.Background(t.SurfacePressed)
					} else if btn.Hovered() {
						btn.Background(t.SurfaceHover)
					} else {
						btn.TextColor(t.TextMuted)
					}
					btn.Children(func() {
						ui.Icon(c, navIcons[page]).Size(14, 14)
						ui.Text(c, page).FontSize(12).FontWeight(map[bool]int{true: 600, false: 500}[active])
					}).OnClick(func() {
						if page == "History" {
							d.history()
						} else {
							d.page = page
						}
					})
				}
			})
		})
		if d.microphoneTesting {
			ui.Row(c).Padding(10, 24).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Box(c).Size(8, 8).Radius(999).Background(t.Warning)
				ui.Text(c, "Microphone test active").FontSize(12).FontWeight(600)
				ui.Text(c, d.microphoneTestStatus).FontSize(12).TextColor(t.TextMuted).Grow(1).SingleLine()
				ui.Button(c, "Stop test").OnClick(func() { d.microphoneTestCancel() })
			})
		}
		ui.Column(c).Grow(1).MinHeight(0).Padding(20, 24).Gap(16).Children(func() {
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
				ui.Icon(c, cloudIcon).Size(13, 13).TextColor(t.TextMuted)
				ui.Text(c, d.syncStatus).FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			if d.snapshot.Error != "" {
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Danger).Children(func() {
					ui.Text(c, d.snapshot.Error).FontSize(11).FontWeight(500).TextColor(t.Danger).SingleLine()
				})
			}
			if d.message != "" {
				ui.Row(c).Padding(3, 10).Radius(999).Border(1, t.Border).Children(func() {
					ui.Text(c, d.message).FontSize(11).FontWeight(500).SingleLine()
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
					ui.Icon(c, micIcon).Size(20, 20)
				})
				ui.Column(c).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, string(s.State)).FontSize(13).FontWeight(600)
						ui.Badge(c, map[bool]string{true: "Both", false: d.settings.CaptureMode}[d.settings.CaptureMode == ""])
					})
					ui.Text(c, fmt.Sprintf("%02d:%02d:%02d", int(s.Duration.Hours()), int(s.Duration.Minutes())%60, int(s.Duration.Seconds())%60)).
						Font("monospace").FontFeatures("tnum").FontSize(30).Bold()
				})
			})
			ui.Spacer(c)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if s.State == app.Idle {
					ui.PrimaryButton(c, "Start recording").Disabled(d.busy || d.microphoneTesting).OnClick(d.start)
				} else {
					if s.State == app.Recording {
						ui.Button(c, "Pause").Disabled(d.busy).OnClick(func() { d.async(d.service.Pause) })
					}
					if s.State == app.Paused {
						ui.PrimaryButton(c, "Resume").Disabled(d.busy).OnClick(func() { d.async(func() error { return d.service.Resume(d.root) }) })
					}
					ui.Button(c, "Stop recording").Disabled(d.busy || s.State == app.Stopping).OnClick(d.stop)
				}
			})
		})
		ui.Divider(c)
		ui.Row(c).Gap(12).Wrap().Children(func() {
			ui.Column(c).Grow(2).MinWidth(240).Gap(5).Children(func() {
				ui.Text(c, "Session title").FontSize(11).FontWeight(600).TextColor(t.TextMuted)
				ui.TextInput(c.Key("title"), &d.title).Placeholder("Meeting title").Disabled(s.State != app.Idle)
			})
			ui.Column(c).Grow(3).MinWidth(280).Gap(5).Children(func() {
				ui.Text(c, "Google Doc target").FontSize(11).FontWeight(600).TextColor(t.TextMuted)
				ui.TextInput(c.Key("start-document"), &d.documentID).Placeholder("Optional existing Google document ID authorized for this app").Disabled(s.State != app.Idle)
			})
		})
		ui.Row(c).Gap(14).Wrap().Children(func() {
			ui.Column(c).Grow(1).MinWidth(220).Padding(10, 14).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, micIcon).Size(13, 13)
					ui.Text(c, "Microphone").FontSize(12).FontWeight(600).MarginStart(6)
					ui.Spacer(c)
					ui.Textf(c, "%d%%", int(s.MicrophoneLevel*100+0.5)).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Meter(c, s.MicrophoneLevel, 0, 1, levels).FillWidth().Label("Microphone level")
			})
			ui.Column(c).Grow(1).MinWidth(220).Padding(10, 14).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, speakerIcon).Size(13, 13)
					ui.Text(c, "System audio").FontSize(12).FontWeight(600).MarginStart(6)
					ui.Spacer(c)
					ui.Textf(c, "%d%%", int(s.SystemLevel*100+0.5)).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Meter(c, s.SystemLevel, 0, 1, levels).FillWidth().Label("System audio level")
			})
		})
	})
	ui.Column(c).Grow(1).MinHeight(0).Padding(18, 20).Gap(12).Radius(t.Radius).Border(1, t.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Live transcript").FontSize(16).Bold()
			ui.Spacer(c)
			segWord := "segments"
			if len(s.Segments) == 1 {
				segWord = "segment"
			}
			ui.Badge(c, fmt.Sprintf("%d %s", len(s.Segments), segWord))
		})
		if len(s.Segments) == 0 && s.Partial == "" {
			ui.Column(c).Grow(1).Center().Gap(8).Padding(24).Radius(t.Radius).Border(1, t.Border).Children(func() {
				ui.Box(c).Size(36, 36).Radius(t.Radius).Border(1, t.Border).Center().Children(func() {
					ui.Icon(c, docIcon).Size(16, 16)
				})
				ui.Text(c, "Waiting for speech").FontSize(14).FontWeight(600)
				ui.Text(c, "Finalized speech is saved on this Mac before Google Docs sync.").FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
			})
		}
		d.liveList.Key = func(i int) any {
			if i < len(s.Segments) {
				return s.Segments[i].ID
			}
			return "partial"
		}
		list := ui.List(c.Key("live"), &d.liveList, len(s.Segments)+1, func(i int) {
			if i < len(s.Segments) {
				ui.Box(c).FillWidth().Padding(3, 0).Children(func() {
					renderSegmentRow(c, s.Segments[i])
				})
			} else if s.Partial != "" {
				ui.Row(c).FillWidth().Padding(10, 12).Gap(10).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
					ui.Badge(c, "LIVE")
					ui.Text(c, s.Partial).Grow(1).FontSize(13).Italic().TextColor(t.TextMuted)
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
func segmentLabel(s transcript.Segment) string {
	return fmt.Sprintf("[%02d:%02d] Speaker %s  %s", s.StartMS/60000, s.StartMS/1000%60, s.Speaker, s.Text)
}
func (d *desktop) historyView(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Grow(1).MinHeight(0).Gap(16).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(330).Shrink(0).Padding(16).Gap(12).Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Meetings").FontSize(15).Bold()
				ui.Spacer(c)
				ui.Badge(c, fmt.Sprintf("%d", len(d.meetings)))
			})
			ui.Row(c).Gap(6).Children(func() {
				ui.TextInput(c.Key("search"), &d.search).Placeholder("Search meetings and transcripts").Grow(1)
				ui.Button(c, "Search").Disabled(d.busy).OnClick(d.history)
			})
			ui.Scroll(c.Key("meetings")).Grow(1).Gap(6).Children(func() {
				if len(d.meetings) == 0 {
					ui.Column(c).Padding(20, 12).Gap(4).Center().Children(func() {
						ui.Text(c, "No meetings found").FontSize(13).FontWeight(600)
						ui.Text(c, "Start a recording or clear the search query.").FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
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
					row.Children(func() {
						ui.Column(c).Grow(1).Gap(4).AlignItems(ui.Start).Children(func() {
							ui.Text(c, m.Title).FontSize(13).FontWeight(600).SingleLine()
							meta := m.CreatedAt.Format("Jan 2, 2006 · 15:04")
							if m.CreatedAt.IsZero() {
								meta = "Saved locally"
							}
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted)
								if m.DocumentID != "" {
									ui.Badge(c, "Synced")
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
					heading := "Transcript reader"
					sub := "Select a meeting on the left to inspect its transcript or link a Google Doc."
					if activeMeeting != nil {
						heading = activeMeeting.Title
						sub = fmt.Sprintf("%d finalized segments", len(d.detail))
					} else if d.selected != "" {
						sub = fmt.Sprintf("Meeting %s · %d segments", d.selected, len(d.detail))
					}
					ui.Text(c, heading).FontSize(16).Bold().SingleLine()
					ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted)
				})
				ui.Button(c, "Copy transcript").Disabled(d.selected == "").OnClick(func() {
					var b strings.Builder
					for _, s := range d.detail {
						b.WriteString(segmentLabel(s) + "\n")
					}
					c.Services().WriteClipboard(b.String())
				})
				ui.Button(c, "Open Google Doc").Disabled(d.selected == "").OnClick(func() {
					for _, m := range d.meetings {
						if m.ID == d.selected && m.DocumentID != "" {
							if err := mygo.Shell.OpenExternal("https://docs.google.com/document/d/" + m.DocumentID + "/edit"); err != nil {
								d.message = err.Error()
							}
							return
						}
					}
					d.message = "This meeting has no associated Google Doc"
				})
			})
			ui.Divider(c)
			ui.Scroll(c.Key("detail")).Grow(1).FillWidth().Gap(8).Children(func() {
				if len(d.detail) == 0 {
					ui.Column(c).Grow(1).Padding(32, 16).Center().Gap(6).Radius(t.Radius).Border(1, t.Border).Children(func() {
						ui.Icon(c, docIcon).Size(20, 20).TextColor(t.TextMuted)
						ui.Text(c, "No transcript segments displayed").FontSize(13).FontWeight(600)
						ui.Text(c, "Choose a recorded meeting from the sidebar to read or copy its speech.").FontSize(12).TextColor(t.TextMuted)
					})
				}
				for _, s := range d.detail {
					renderSegmentRow(c, s)
				}
			})
			ui.Row(c).Padding(12).Gap(10).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Icon(c, docIcon).Size(15, 15).TextColor(t.TextMuted)
				ui.TextInput(c.Key("document"), &d.documentID).Placeholder("Existing Google document ID").Grow(1).MinWidth(200)
				ui.Button(c, "Use document for selected meeting").Disabled(d.selected == "" || d.busy).OnClick(func() {
					meeting, id := d.selected, strings.TrimSpace(d.documentID)
					if id == "" {
						d.message = "Enter a document ID"
						return
					}
					d.async(func() error {
						if err := d.docs.CheckDocument(d.root, id); err != nil {
							return err
						}
						return d.store.SetDocument(d.root, meeting, id)
					})
				})
			})
		})
	})
}
func (d *desktop) settingsView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c.Key("settings")).Grow(1).FillWidth().Gap(16).Children(func() {
		ui.Row(c).Padding(12, 16).Gap(12).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, keyIcon).Size(16, 16).TextColor(t.TextMuted)
			ui.Text(c, "Credentials stay in macOS Keychain. Transcripts stay on this Mac.").FontSize(12).FontWeight(500).Grow(1)
			ui.PrimaryButton(c, "Save settings").Disabled(d.busy || d.microphoneTesting || d.snapshot.State != app.Idle && d.snapshot.State != app.Paused).OnClick(d.save)
		})
		ui.Row(c).Gap(16).AlignItems(ui.Stretch).Wrap().Children(func() {
			surfaceCard(c, func() {
				cardHeader(c, keyIcon, "Cloud & Authentication", "Soniox speech recognition and Google Docs OAuth credentials.")
				ui.Divider(c)
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Soniox API key").FontSize(12).FontWeight(600)
					ui.TextInput(c.Key("key"), &d.key).Password().Placeholder("Enter a key to replace the saved key")
				})
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Google desktop OAuth client ID").FontSize(12).FontWeight(600)
					ui.TextInput(c.Key("google-client"), &d.settings.GoogleClientID)
				})
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Google desktop OAuth client secret").FontSize(12).FontWeight(600)
					ui.TextInput(c.Key("google-secret"), &d.googleSecret).Password().Placeholder("Saved to Keychain for this client ID")
				})
				ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Button(c, "Connect Google").Disabled(d.busy || d.snapshot.State != app.Idle).OnClick(func() {
						id := d.settings.GoogleClientID
						d.replaceGoogle(id)
						g := d.currentGoogle()
						d.async(func() error {
							ctx, cancel := context.WithTimeout(d.root, 3*time.Minute)
							defer cancel()
							return g.Connect(ctx)
						})
					})
					ui.Text(c, "Save settings before connecting.").FontSize(12).TextColor(t.TextMuted)
				})
				ui.Divider(c)
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Language hints, separated by commas").FontSize(12).FontWeight(600)
					ui.TextInput(c.Key("languages"), &d.settings.Languages)
				})
				ui.Checkbox(c.Key("auto-sync"), &d.settings.AutoSync, "Create a Google Doc when recording starts")
			}).Grow(1).Basis(380).MinWidth(320)

			surfaceCard(c, func() {
				cardHeader(c, micIcon, "Audio & Capture Hardware", "Select inputs, verify signal levels, and configure the native helper.")
				ui.Divider(c)
				ui.Column(c).Gap(6).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "Microphone").FontSize(12).FontWeight(600)
						ui.Spacer(c)
						ui.Button(c, "Refresh microphones").Disabled(d.busy || d.microphoneTesting).OnClick(d.refreshDevices)
					})
					options := []string{"System default"}
					for _, device := range d.devices {
						options = append(options, deviceLabel(device))
					}
					ui.Select(c.Key("microphone"), &d.microphone, options).FillWidth()
				})
				ui.Column(c).Padding(12).Gap(8).Radius(t.Radius).Border(1, t.Border).Children(func() {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
						if d.microphoneTesting {
							ui.Button(c, "Stop microphone test").OnClick(func() { d.microphoneTestCancel() })
						} else {
							ui.Button(c, "Test microphone for 10 seconds").Disabled(d.busy || d.snapshot.State != app.Idle).OnClick(d.testMicrophone)
						}
						ui.Meter(c, d.microphoneTestLevel, 0, 1, &ui.MeterLevels{Warning: 0.75, Critical: 0.92}).Grow(1).MinWidth(140).Label("Microphone test level")
					})
					if d.microphoneTestStatus != "" {
						ui.Text(c, d.microphoneTestStatus).FontSize(12).FontWeight(500)
					}
					ui.Text(c, "This test stays on your Mac. It does not create a meeting or contact Soniox.").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Audio sources").FontSize(12).FontWeight(600)
					ui.Select(c.Key("capture-mode"), &d.settings.CaptureMode, []string{"Both", "Microphone only", "System audio only"}).FillWidth()
				})
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, "Audio helper executable").FontSize(12).FontWeight(600)
					ui.TextInput(c.Key("helper"), &d.settings.HelperPath)
				})
				ui.Text(c, "Use headphones to reduce duplicate speech from microphone echo. Pause before changing capture settings.").FontSize(11).TextColor(t.TextMuted)
			}).Grow(1).Basis(380).MinWidth(320)
		})
	})
}

func (d *desktop) ready() {
	d.win = mygo.NewWindow(mygo.WindowOptions{Title: "Titen", Width: 1000, Height: 760, MinWidth: 720, MinHeight: 560, StateKey: "main", Content: ui.View(d.view)})
	d.win.OnClose(func(e *mygo.CloseEvent) {
		if !d.quitting {
			e.PreventDefault()
			d.win.Hide()
		}
	})
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Open Titen", Click: func(*mygo.MenuItem, *mygo.Window) { d.win.Show(); d.win.Focus() }},
		{Label: "Start recording", Click: func(*mygo.MenuItem, *mygo.Window) { d.start() }},
		{Label: "Pause recording", Click: func(*mygo.MenuItem, *mygo.Window) { d.async(d.service.Pause) }},
		{Label: "Resume recording", Click: func(*mygo.MenuItem, *mygo.Window) { d.async(func() error { return d.service.Resume(d.root) }) }},
		{Label: "Stop recording", Click: func(*mygo.MenuItem, *mygo.Window) { d.stop() }},
		mygo.Separator(), {Label: "Quit Titen", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	var err error
	d.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(), IconIsTemplate: true, ToolTip: "Titen · Ready", Menu: menu})
	if err != nil {
		d.message = "Menu bar unavailable. " + err.Error()
	}
	worker := docsync.Worker{Store: d.store, Docs: d.docs, OnStatus: func(status string) { d.win.Update(func() { d.syncStatus = status }) }}
	d.tasks.Add(1)
	go func() { defer d.tasks.Done(); worker.Run(d.root) }()
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
						if d.microphoneTesting {
							d.tray.SetToolTip("Titen · Microphone test active")
						} else {
							d.tray.SetToolTip("Titen · " + string(s.State))
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
			d.message = "Wait for the current operation before quitting"
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
func trayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 24, 24))
	for y := 3; y < 21; y++ {
		for x := 4; x < 20; x++ {
			if x < 7 || x > 16 || y < 6 || y > 17 {
				img.SetNRGBA(x, y, color.NRGBA{A: 255})
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
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
	cfg, err := app.LoadSettings(dir)
	if err != nil {
		slog.Error("settings", "error", err)
		os.Exit(1)
	}
	if cfg.HelperPath == "" {
		exe, _ := os.Executable()
		cfg.HelperPath = filepath.Join(filepath.Dir(exe), "titen-audio")
		if _, err = os.Stat(cfg.HelperPath); err != nil {
			cfg.HelperPath = filepath.Join("native", "macos", ".build", "release", "titen-audio")
		}
	}
	store, err := storage.Open(filepath.Join(dir, "titen.sqlite3"))
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	google := &auth.Google{ClientID: cfg.GoogleClientID, ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"), ClientSecretLookup: func() (string, error) { return auth.GetSecret("google-client-secret/" + cfg.GoogleClientID) }, OpenBrowser: func(url string) error { return exec.Command("open", url).Run() }}
	d := &desktop{service: app.NewService(store), store: store, dir: dir, settings: cfg, page: "Record", syncStatus: "Google Docs sync idle", root: ctx, cancel: cancel, microphone: "System default", google: google}
	d.docs = &docsync.GoogleDocs{Token: func(ctx context.Context) (string, error) { return d.currentGoogle().Token(ctx) }}
	d.liveList.FollowEnd = true
	if cfg.MicrophoneID != "" {
		d.microphone = cfg.MicrophoneID
	}
	mygo.App.SetName("Titen")
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

var (
	micIcon      = svgIcon(`<path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><path d="M12 19v3"/><path d="M8 22h8"/>`)
	historyIcon  = svgIcon(`<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/>`)
	settingsIcon = svgIcon(`<circle cx="12" cy="12" r="3"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/>`)
	speakerIcon  = svgIcon(`<path d="M11 5 6 9H2v6h4l5 4V5Z"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/>`)
	docIcon      = svgIcon(`<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M8 13h8"/><path d="M8 17h6"/>`)
	copyIcon     = svgIcon(`<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`)
	keyIcon      = svgIcon(`<circle cx="7.5" cy="15.5" r="4.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/>`)
	cloudIcon    = svgIcon(`<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>`)
)

func svgIcon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
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
				ui.Icon(c, icon).Size(15, 15)
			})
		}
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(15).FontWeight(600)
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(12).TextColor(t.TextMuted)
			}
		})
	})
}

func renderSegmentRow(c *ui.Context, s transcript.Segment) {
	t := c.Theme()
	stamp := fmt.Sprintf("%02d:%02d", s.StartMS/60000, s.StartMS/1000%60)
	who := strings.TrimSpace(s.Speaker)
	if who == "" {
		who = "1"
	}
	ui.Row(c).FillWidth().Padding(10, 12).Gap(12).Radius(t.Radius).Border(1, t.Border).AlignItems(ui.Start).Children(func() {
		ui.Box(c).Padding(2, 7).Radius(t.Radius - 2).Border(1, t.Border).Shrink(0).Children(func() {
			ui.Text(c, stamp).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(t.TextMuted)
		})
		ui.Badge(c, "Speaker "+who)
		ui.Text(c, s.Text).Grow(1).FontSize(13).LineHeight(1.45).
			Label(segmentLabel(s))
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
	d.microphoneTestStatus = "Speak into the selected microphone. Testing for up to 10 seconds."
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
				d.microphoneTestStatus = "Microphone test finished. No clear signal detected. Check the selected input and speak closer to it."
			default:
				d.microphoneTestStatus = "Microphone test finished. Input signal detected."
			}
		})
	}()
}
