package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// APIDUO:file-relay:BEGIN
//
// FileObject maps a gateway-issued file id onto the upstream id that actually
// owns the bytes.
//
// The indirection is not cosmetic. An upstream file id is only meaningful to
// the account that created it, several providers hand out colliding prefixes
// (OpenAI and Moonshot both use "file-"), and a relayed download must never be
// served to a user who did not upload it. Keeping our own id means every
// lookup is scoped by user_id, and the channel that produced the file is known
// before any outbound request is made.
type FileObject struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	GatewayId string `json:"gateway_id" gorm:"type:varchar(80);uniqueIndex;not null"`
	UserId    int    `json:"user_id" gorm:"index:idx_file_object_owner,priority:1"`
	TokenId   int    `json:"token_id"`
	ChannelId int    `json:"channel_id" gorm:"index"`
	// UpstreamId is empty when the provider answers with parsed text instead of
	// a retrievable object (Zhipu's synchronous parser); InlineContent then
	// holds the payload.
	UpstreamId string `json:"-" gorm:"type:varchar(191);index"`
	Purpose    string `json:"purpose" gorm:"type:varchar(32);index:idx_file_object_owner,priority:2"`
	Filename   string `json:"filename" gorm:"type:varchar(255)"`
	Bytes      int64  `json:"bytes"`
	MimeType   string `json:"-" gorm:"type:varchar(127)"`
	// ContentKind tells the download handler what it is about to stream:
	// "bytes" for the original upload, "text" for a provider-side extraction.
	ContentKind string `json:"-" gorm:"type:varchar(16);default:'bytes'"`
	// Status is what the provider reported (ready / processed / uploaded). It is
	// echoed back rather than re-invented, so a client that checks the value
	// sees the same vocabulary its own provider uses.
	Status string `json:"status" gorm:"type:varchar(32)"`
	// UpstreamCreatedAt is the provider's timestamp. CreatedAt stays the moment
	// the gateway accepted the file, because the daily upload window is measured
	// against it and a drifting provider clock must not skew the count.
	UpstreamCreatedAt int64 `json:"-" gorm:"bigint"`
	// InlineContent keeps a provider extraction that cannot be fetched back by
	// id. It is never serialised to clients directly.
	InlineContent string `json:"-" gorm:"type:mediumtext"`
	ExpiresAt     int64  `json:"expires_at" gorm:"bigint;index"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
}

const (
	FileContentKindBytes = "bytes"
	FileContentKindText  = "text"
)

// GenerateGatewayFileId returns an id that cannot be mistaken for a provider
// id: both OpenAI and Moonshot mint "file-..." values, so the gateway marks its
// own as "file-apiduo-...".
func GenerateGatewayFileId() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "file-apiduo-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "file-apiduo-" + hex.EncodeToString(buf)
}

func CreateFileObject(object *FileObject) error {
	if object == nil || object.GatewayId == "" {
		return errors.New("file object requires a gateway id")
	}
	if object.CreatedAt == 0 {
		object.CreatedAt = common.GetTimestamp()
	}
	return DB.Create(object).Error
}

// GetFileObjectByGatewayId resolves an id inside the caller's own namespace.
// The user_id condition is the authorization check: an id belonging to another
// account must look exactly like a missing one.
func GetFileObjectByGatewayId(userId int, gatewayId string) (*FileObject, bool, error) {
	if userId <= 0 || gatewayId == "" {
		return nil, false, nil
	}
	var object FileObject
	err := DB.Where("gateway_id = ? AND user_id = ?", gatewayId, userId).First(&object).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &object, true, nil
}

type FileObjectListQuery struct {
	UserId  int
	Purpose string
	After   string
	Order   string
	Limit   int
}

// ListFileObjects answers from the gateway's own records rather than paging the
// upstream: providers disagree on cursor semantics, and a list assembled
// locally always shows gateway ids that remain valid.
func ListFileObjects(query FileObjectListQuery) ([]FileObject, bool, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	descending := query.Order != "asc"

	// Expired rows are hidden from the listing for the same reason they stop
	// resolving: a client that pages the list must not be handed ids that 404.
	tx := DB.Model(&FileObject{}).
		Where("user_id = ?", query.UserId).
		Where("expires_at = 0 OR expires_at > ?", common.GetTimestamp())
	if query.Purpose != "" {
		tx = tx.Where("purpose = ?", query.Purpose)
	}
	if query.After != "" {
		var anchor FileObject
		err := DB.Select("id").
			Where("gateway_id = ? AND user_id = ?", query.After, query.UserId).
			Where("expires_at = 0 OR expires_at > ?", common.GetTimestamp()).
			First(&anchor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if descending {
			tx = tx.Where("id < ?", anchor.Id)
		} else {
			tx = tx.Where("id > ?", anchor.Id)
		}
	}
	if descending {
		tx = tx.Order("id DESC")
	} else {
		tx = tx.Order("id ASC")
	}

	var objects []FileObject
	// One extra row tells the caller whether another page exists without a
	// second count query.
	if err := tx.Limit(limit + 1).Find(&objects).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(objects) > limit
	if hasMore {
		objects = objects[:limit]
	}
	return objects, hasMore, nil
}

func DeleteFileObject(userId int, gatewayId string) (bool, error) {
	if userId <= 0 || gatewayId == "" {
		return false, nil
	}
	result := DB.Where("gateway_id = ? AND user_id = ?", gatewayId, userId).Delete(&FileObject{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// DeleteExpiredFileObjectsOfUser prunes one account's expired rows.
//
// The sweep is scoped to a single user because it runs on the upload path
// rather than in a background worker: an expired row still counts against that
// user's storage ceiling, and a table-wide delete on every upload would turn a
// cheap guard into a scan.
//
// The companion upstream delete is intentionally not attempted: the provider
// expires the object on its own schedule, and a failing provider call must not
// stall the sweep.
func DeleteExpiredFileObjectsOfUser(userId int, now int64) (int64, error) {
	if userId <= 0 {
		return 0, nil
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	result := DB.Where("user_id = ? AND expires_at > 0 AND expires_at < ?", userId, now).Delete(&FileObject{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// FileUsage is the file footprint of one account. Count is scoped by the
// caller's time window; TotalBytes is always the current holding, because the
// storage limit is about what is still taking up room, not what was ever
// uploaded.
type FileUsage struct {
	Count      int64 `gorm:"column:count"`
	TotalBytes int64 `gorm:"column:total_bytes"`
}

// GetFileUsage reports how many files the user created since `since` (all files
// when since is zero) and how many bytes those files hold.
func GetFileUsage(userId int, since int64) (FileUsage, error) {
	var usage FileUsage
	if userId <= 0 {
		return usage, nil
	}
	query := DB.Model(&FileObject{}).
		Select("COUNT(*) AS count, COALESCE(SUM(bytes), 0) AS total_bytes").
		Where("user_id = ?", userId)
	if since > 0 {
		query = query.Where("created_at >= ?", since)
	}
	err := query.Scan(&usage).Error
	return usage, err
}

// APIDUO:file-relay:END
