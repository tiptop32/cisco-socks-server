package tui

import (
	"fmt"
	"time"

	"github.com/jroimartin/gocui"

	"github.com/merzzzl/cisco-socks-server/internal/service"
	"github.com/merzzzl/cisco-socks-server/internal/utils/log"
)

func setupStatus(g *gocui.Gui, svc *service.Service, done <-chan struct{}, maxX, maxY int) error {
	v, err := g.SetView("status", maxX-sidebarWidth+1, 3, maxX-1, maxY-1)
	if err != nil {
		if !isNewView(err) {
			return err
		}

		v.Title = " Status "

		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					g.Update(func(*gocui.Gui) error {
						v.Clear()

						state := svc.GetState()

						fmt.Fprintf(v, " VPN    %s\n", indicator(state.CiscoConnected))
						fmt.Fprintf(v, " PF     %s\n", indicator(state.PFDisabled))
						fmt.Fprintf(v, " Bind   %s\n", bindLabel(state.LANInterface))
						fmt.Fprintf(v, " Proxy  %s\n", indicator(state.ProxyStarted))
						fmt.Fprintf(v, " DNS    %s\n", indicator(state.DNSStarted))

						return nil
					})
				}
			}
		}()
	}

	return nil
}

func indicator(ok bool) string {
	if ok {
		return log.Colorize("● OK", 10)
	}

	return log.Colorize("○ --", 9)
}

func bindLabel(iface string) string {
	if iface == "" {
		return log.Colorize("○ --", 9)
	}

	return log.Colorize("● "+iface, 10)
}
