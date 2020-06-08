package channel

import (
	"errors"
	"github.com/gofrs/uuid"
	"github.com/traPtitech/traQ/model"
	"github.com/traPtitech/traQ/repository"
)

var (
	ErrChannelNotFound      = errors.New("channel not found")
	ErrChannelNameConflicts = errors.New("channel name conflicts")
	ErrInvalidChannelName   = errors.New("invalid channel name")
	ErrInvalidParentChannel = errors.New("invalid parent channel")
	ErrTooDeepChannel       = errors.New("too deep channel")
	ErrChannelArchived      = errors.New("channel archived")
	ErrForcedNotification   = errors.New("forced notification channel")
	ErrInvalidChannel       = errors.New("invalid channel")
)

type Manager interface {
	GetChannel(id uuid.UUID) (*model.Channel, error)
	CreatePublicChannel(name string, parent, creatorID uuid.UUID) (*model.Channel, error)
	UpdateChannel(id uuid.UUID, args repository.UpdateChannelArgs) error
	PublicChannelTree() Tree

	ChangeChannelSubscriptions(channelID uuid.UUID, subscriptions map[uuid.UUID]model.ChannelSubscribeLevel, keepOffLevel bool, updaterID uuid.UUID) error

	GetDMChannel(user1, user2 uuid.UUID) (*model.Channel, error)
	GetDMChannelMembers(id uuid.UUID) ([]uuid.UUID, error)
	GetDMChannelMapping(userID uuid.UUID) (map[uuid.UUID]uuid.UUID, error)

	IsChannelAccessibleToUser(userID, channelID uuid.UUID) (bool, error)
	IsPublicChannel(id uuid.UUID) bool

	Shutdown()
}
