package minecraft

import (
	"context"
	"errors"
	"strings"
	"time"

	"gamenode/internal/console"
	"gamenode/internal/servers"
)

// ScheduledRestarter adapts the existing persistent local schedule runner to
// Minecraft's save-before-stop requirement. It adds no process/runtime path:
// servers.Service still owns every stop and start.
type ScheduledRestarter struct {
	Servers     *servers.Service
	Console     *console.Manager
	SaveTimeout time.Duration
}

func (r ScheduledRestarter) Restart(ctx context.Context, id string) (servers.Record, error) {
	record, err := r.Servers.Get(ctx, id)
	if err != nil {
		return servers.Record{}, err
	}
	if record.Runtime.CurrentState != servers.StateRunning {
		return record, errors.New("scheduled restart skipped because the server is not running")
	}
	templateID, _ := r.Servers.TemplateID(ctx, id)
	if !strings.Contains(strings.ToLower(templateID), "minecraft") && !strings.Contains(strings.ToLower(templateID), "neoforge") {
		return r.Servers.Restart(ctx, id)
	}
	session, attached := r.Console.CurrentSession(id)
	if !attached || session == nil {
		return record, errors.New("Minecraft console is detached; save-before-restart is unavailable")
	}
	events, unsubscribe := session.Subscribe()
	defer unsubscribe()
	sentAt := time.Now().UTC()
	if err = session.Input(record.Server.ConsoleInput("save-all flush\n")); err != nil {
		return record, errors.New("Minecraft save command could not be delivered")
	}
	timeout := r.SaveTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return record, ctx.Err()
		case <-timer.C:
			return record, errors.New("Minecraft did not confirm save-all flush before the timeout")
		case event, open := <-events:
			if !open || event.Type == "state" && event.State != "running" {
				return record, errors.New("Minecraft exited while saving before restart")
			}
			if event.Timestamp.After(sentAt) && strings.Contains(strings.ToLower(event.Data), "saved the game") {
				return r.Servers.Restart(ctx, id)
			}
		}
	}
}
