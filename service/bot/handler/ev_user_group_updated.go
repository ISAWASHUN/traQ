package handler

import (
	"fmt"
	"time"

	"github.com/leandro-lugaresi/hub"

	"github.com/traPtitech/traQ/model"
	"github.com/traPtitech/traQ/service/bot/event"
	"github.com/traPtitech/traQ/service/bot/event/payload"
)

func UserGroupUpdated(ctx Context, datetime time.Time, _ string, fields hub.Fields) error {
	group := fields["group"].(model.UserGroup)
	bots, err := ctx.GetBots(event.UserGroupUpdated)
	if err != nil {
		return fmt.Errorf("failed to GetBots: %w", err)
	}
	if len(bots) == 0 {
		return nil
	}

	if err := ctx.Multicast(
		event.UserGroupUpdated,
		payload.MakeUserGroupUpdated(datetime, group),
		bots,
	); err != nil {
		return fmt.Errorf("failed to multicast: %w", err)
	}
	return nil
}
