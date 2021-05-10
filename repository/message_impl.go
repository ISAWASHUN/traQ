package repository

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/leandro-lugaresi/hub"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/traPtitech/traQ/event"
	"github.com/traPtitech/traQ/model"
	"github.com/traPtitech/traQ/utils/message"
)

// CreateMessage implements MessageRepository interface.
func (repo *GormRepository) CreateMessage(userID, channelID uuid.UUID, text string) (*model.Message, error) {
	if userID == uuid.Nil || channelID == uuid.Nil {
		return nil, ErrNilID
	}

	m := &model.Message{
		ID:        uuid.Must(uuid.NewV4()),
		UserID:    userID,
		ChannelID: channelID,
		Text:      text,
		Stamps:    []model.MessageStamp{},
	}
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(m).Error; err != nil {
			return err
		}

		clm := &model.ChannelLatestMessage{
			ChannelID: m.ChannelID,
			MessageID: m.ID,
			DateTime:  m.CreatedAt,
		}

		return tx.
			Clauses(clause.OnConflict{UpdateAll: true}).
			Create(clm).
			Error
	})
	if err != nil {
		return nil, err
	}

	parseResult := message.Parse(text)
	repo.hub.Publish(hub.Message{
		Name: event.MessageCreated,
		Fields: hub.Fields{
			"message_id":   m.ID,
			"message":      m,
			"parse_result": parseResult,
		},
	})
	if len(parseResult.Citation) > 0 {
		repo.hub.Publish(hub.Message{
			Name: event.MessageCited,
			Fields: hub.Fields{
				"message_id": m.ID,
				"message":    m,
				"cited_ids":  parseResult.Citation,
			},
		})
	}
	return m, nil
}

// UpdateMessage implements MessageRepository interface.
func (repo *GormRepository) UpdateMessage(messageID uuid.UUID, text string) error {
	if messageID == uuid.Nil {
		return ErrNilID
	}

	var (
		old model.Message
		new model.Message
	)
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&old, &model.Message{ID: messageID}).Error; err != nil {
			return convertError(err)
		}

		// archiving
		if err := tx.Create(&model.ArchivedMessage{
			ID:        uuid.Must(uuid.NewV4()),
			MessageID: old.ID,
			UserID:    old.UserID,
			Text:      old.Text,
			DateTime:  old.UpdatedAt,
		}).Error; err != nil {
			return err
		}

		// update
		if err := tx.Model(&old).Update("text", text).Error; err != nil {
			return err
		}

		return tx.Where(&model.Message{ID: messageID}).First(&new).Error
	})
	if err != nil {
		return err
	}
	repo.hub.Publish(hub.Message{
		Name: event.MessageUpdated,
		Fields: hub.Fields{
			"message_id":  messageID,
			"old_message": &old,
			"message":     &new,
		},
	})
	return nil
}

// DeleteMessage implements MessageRepository interface.
func (repo *GormRepository) DeleteMessage(messageID uuid.UUID) error {
	if messageID == uuid.Nil {
		return ErrNilID
	}

	var (
		m       model.Message
		unreads []*model.Unread
	)
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(&model.Message{ID: messageID}).First(&m).Error; err != nil {
			return convertError(err)
		}

		if err := tx.Find(&unreads, &model.Unread{MessageID: messageID}).Error; err != nil {
			return err
		}

		if err := tx.Delete(&m).Error; err != nil {
			return err
		}
		if err := tx.Delete(model.Unread{}, &model.Unread{MessageID: messageID}).Error; err != nil {
			return err
		}
		if err := tx.Delete(model.Pin{}, &model.Pin{MessageID: messageID}).Error; err != nil {
			return err
		}
		if err := tx.Delete(model.ClipFolderMessage{}, &model.ClipFolderMessage{MessageID: messageID}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	repo.hub.Publish(hub.Message{
		Name: event.MessageDeleted,
		Fields: hub.Fields{
			"message_id":      messageID,
			"message":         &m,
			"deleted_unreads": unreads,
		},
	})
	return nil
}

// GetMessageByID implements MessageRepository interface.
func (repo *GormRepository) GetMessageByID(messageID uuid.UUID) (*model.Message, error) {
	if messageID == uuid.Nil {
		return nil, ErrNotFound
	}
	message := &model.Message{}
	if err := repo.db.Scopes(messagePreloads).Where(&model.Message{ID: messageID}).Take(message).Error; err != nil {
		return nil, convertError(err)
	}
	return message, nil
}

// GetMessages implements MessageRepository interface.
func (repo *GormRepository) GetMessages(query MessagesQuery) (messages []*model.Message, more bool, err error) {
	messages = make([]*model.Message, 0)

	tx := repo.db
	if !query.DisablePreload {
		tx = tx.Scopes(messagePreloads)
	}

	if query.Asc {
		tx = tx.Order("messages.created_at")
	} else {
		tx = tx.Order("messages.created_at DESC")
	}

	if query.Offset > 0 {
		tx = tx.Offset(query.Offset)
	}

	if query.ExcludeDMs && query.Channel == uuid.Nil && query.User == uuid.Nil && query.ChannelsSubscribedByUser == uuid.Nil && !query.Since.Valid && !query.Until.Valid && query.Limit > 0 {
		// アクティビティ用にUSE INDEX指定でクエリ発行
		// TODO 綺麗じゃない
		err = tx.
			Limit(query.Limit + 1).
			Raw("SELECT messages.* FROM messages USE INDEX (idx_messages_deleted_at_created_at) INNER JOIN channels ON messages.channel_id = channels.id WHERE messages.deleted_at IS NULL AND channels.is_public = true").
			Scan(&messages).
			Error
		if len(messages) > query.Limit {
			return messages[:len(messages)-1], true, err
		}
		return messages, false, err
	}

	if query.ChannelsSubscribedByUser != uuid.Nil || query.ExcludeDMs {
		tx = tx.Joins("INNER JOIN channels ON messages.channel_id = channels.id")
	}

	if query.Channel != uuid.Nil {
		tx = tx.Where("messages.channel_id = ?", query.Channel)
	}
	if query.User != uuid.Nil {
		tx = tx.Where("messages.user_id = ?", query.User)
	}
	if query.ChannelsSubscribedByUser != uuid.Nil {
		tx = tx.Where("channels.is_forced = TRUE OR channels.id IN (SELECT s.channel_id FROM users_subscribe_channels s WHERE s.user_id = ?)", query.ChannelsSubscribedByUser)
	}

	if query.Inclusive {
		if query.Since.Valid {
			tx = tx.Where("messages.created_at >= ?", query.Since.Time)
		}
		if query.Until.Valid {
			tx = tx.Where("messages.created_at <= ?", query.Until.Time)
		}
	} else {
		if query.Since.Valid {
			tx = tx.Where("messages.created_at > ?", query.Since.Time)
		}
		if query.Until.Valid {
			tx = tx.Where("messages.created_at < ?", query.Until.Time)
		}
	}

	if query.ExcludeDMs {
		tx = tx.Where("channels.is_public = true")
	}

	if query.Limit > 0 {
		err = tx.Limit(query.Limit + 1).Find(&messages).Error
		if len(messages) > query.Limit {
			return messages[:len(messages)-1], true, err
		}
	} else {
		err = tx.Find(&messages).Error
	}
	return messages, false, err
}

// GetUpdatedMessagesAfter implements MessageRepository interface.
func (repo *GormRepository) GetUpdatedMessagesAfter(after time.Time, limit int) (messages []*model.Message, more bool, err error) {
	err = repo.db.
		Limit(limit+1).
		Raw("SELECT * FROM `messages` USE INDEX (idx_messages_deleted_at_updated_at) WHERE `messages`.`deleted_at` IS NULL AND `messages`.`updated_at` > ? ORDER BY `messages`.`updated_at`", after).
		Scan(&messages).
		Error

	if len(messages) > limit {
		more = true
		messages = messages[:limit]
	}
	return
}

// GetDeletedMessagesAfter implements MessageRepository interface.
func (repo *GormRepository) GetDeletedMessagesAfter(after time.Time, limit int) (messages []*model.Message, more bool, err error) {
	err = repo.db.
		Limit(limit+1).
		Raw("SELECT * FROM `messages` USE INDEX (idx_messages_deleted_at_updated_at) WHERE `messages`.`deleted_at` > ? ORDER BY `messages`.`deleted_at`", after).
		Scan(&messages).
		Error

	if len(messages) > limit {
		more = true
		messages = messages[:limit]
	}
	return
}

// SetMessageUnread implements MessageRepository interface.
func (repo *GormRepository) SetMessageUnread(userID, messageID uuid.UUID, noticeable bool) error {
	if userID == uuid.Nil || messageID == uuid.Nil {
		return ErrNilID
	}

	var update bool
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		var u model.Unread
		if err := tx.First(&u, &model.Unread{UserID: userID, MessageID: messageID}).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return tx.Create(&model.Unread{UserID: userID, MessageID: messageID, Noticeable: noticeable}).Error
			}
			return err
		}
		update = true
		return tx.Model(&u).Update("noticeable", noticeable).Error
	})
	if err != nil {
		return err
	}
	if !update {
		repo.hub.Publish(hub.Message{
			Name: event.MessageUnread,
			Fields: hub.Fields{
				"message_id": messageID,
				"user_id":    userID,
				"noticeable": noticeable,
			},
		})
	}
	return nil
}

// GetUnreadMessagesByUserID implements MessageRepository interface.
func (repo *GormRepository) GetUnreadMessagesByUserID(userID uuid.UUID) (unreads []*model.Message, err error) {
	unreads = make([]*model.Message, 0)
	if userID == uuid.Nil {
		return unreads, nil
	}
	err = repo.db.
		Joins("INNER JOIN unreads ON unreads.message_id = messages.id AND unreads.user_id = ?", userID.String()).
		Order("messages.created_at").
		Find(&unreads).
		Error
	return unreads, err
}

// GetUserUnreadChannels implements MessageRepository interface.
func (repo *GormRepository) GetUserUnreadChannels(userID uuid.UUID) ([]*UserUnreadChannel, error) {
	res := make([]*UserUnreadChannel, 0)
	if userID == uuid.Nil {
		return res, nil
	}
	return res, repo.db.Raw(`SELECT m.channel_id AS channel_id, COUNT(m.id) AS count, MAX(u.noticeable) AS noticeable, MIN(m.created_at) AS since, MAX(m.created_at) AS updated_at FROM unreads u JOIN messages m on u.message_id = m.id WHERE u.user_id = ? GROUP BY m.channel_id`, userID).Scan(&res).Error
}

// DeleteUnreadsByChannelID implements MessageRepository interface.
func (repo *GormRepository) DeleteUnreadsByChannelID(channelID, userID uuid.UUID) error {
	if channelID == uuid.Nil || userID == uuid.Nil {
		return ErrNilID
	}
	result := repo.db.Exec("DELETE unreads FROM unreads INNER JOIN messages ON unreads.user_id = ? AND unreads.message_id = messages.id WHERE messages.channel_id = ?", userID, channelID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		repo.hub.Publish(hub.Message{
			Name: event.ChannelRead,
			Fields: hub.Fields{
				"channel_id":        channelID,
				"user_id":           userID,
				"read_messages_num": int(result.RowsAffected),
			},
		})
	}
	return nil
}

// GetChannelLatestMessagesByUserID implements MessageRepository interface.
func (repo *GormRepository) GetChannelLatestMessagesByUserID(userID uuid.UUID, limit int, subscribeOnly bool) ([]*model.Message, error) {
	var query string
	switch {
	case subscribeOnly:
		query = `SELECT m.id, m.user_id, m.channel_id, m.text, m.created_at, m.updated_at, m.deleted_at FROM channel_latest_messages clm INNER JOIN messages m ON clm.message_id = m.id INNER JOIN channels c ON clm.channel_id = c.id WHERE c.deleted_at IS NULL AND c.is_public = TRUE AND m.deleted_at IS NULL AND (c.is_forced = TRUE OR c.id IN (SELECT s.channel_id FROM users_subscribe_channels s WHERE s.user_id = 'USER_ID')) ORDER BY clm.date_time DESC`
		query = strings.Replace(query, "USER_ID", userID.String(), -1)
	default:
		query = `SELECT m.id, m.user_id, m.channel_id, m.text, m.created_at, m.updated_at, m.deleted_at FROM channel_latest_messages clm INNER JOIN messages m ON clm.message_id = m.id INNER JOIN channels c ON clm.channel_id = c.id WHERE c.deleted_at IS NULL AND c.is_public = TRUE AND m.deleted_at IS NULL ORDER BY clm.date_time DESC`
	}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	result := make([]*model.Message, 0)
	return result, repo.db.Raw(query).Scan(&result).Error
}

// AddStampToMessage implements MessageRepository interface.
func (repo *GormRepository) AddStampToMessage(messageID, stampID, userID uuid.UUID, count int) (ms *model.MessageStamp, err error) {
	if messageID == uuid.Nil || stampID == uuid.Nil || userID == uuid.Nil {
		return nil, ErrNilID
	}

	err = repo.db.
		Clauses(clause.OnConflict{
			DoUpdates: clause.Assignments(map[string]interface{}{
				"count":      gorm.Expr(fmt.Sprintf("count + %d", count)),
				"updated_at": gorm.Expr("now()"),
			}),
		}).
		Create(&model.MessageStamp{MessageID: messageID, StampID: stampID, UserID: userID, Count: count}).
		Error
	if err != nil {
		return nil, err
	}

	// 楽観的に取得し直す。
	ms = &model.MessageStamp{}
	if err := repo.db.Take(ms, &model.MessageStamp{MessageID: messageID, StampID: stampID, UserID: userID}).Error; err != nil {
		return nil, err
	}
	repo.hub.Publish(hub.Message{
		Name: event.MessageStamped,
		Fields: hub.Fields{
			"message_id": messageID,
			"stamp_id":   stampID,
			"user_id":    userID,
			"count":      ms.Count,
			"created_at": ms.CreatedAt,
		},
	})
	return ms, nil
}

// RemoveStampFromMessage implements MessageRepository interface.
func (repo *GormRepository) RemoveStampFromMessage(messageID, stampID, userID uuid.UUID) (err error) {
	if messageID == uuid.Nil || stampID == uuid.Nil || userID == uuid.Nil {
		return ErrNilID
	}
	result := repo.db.Delete(&model.MessageStamp{MessageID: messageID, StampID: stampID, UserID: userID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		repo.hub.Publish(hub.Message{
			Name: event.MessageUnstamped,
			Fields: hub.Fields{
				"message_id": messageID,
				"stamp_id":   stampID,
				"user_id":    userID,
			},
		})
	}
	return nil
}

func messagePreloads(db *gorm.DB) *gorm.DB {
	return db.
		Preload("Stamps").
		Preload("Pin")
}
