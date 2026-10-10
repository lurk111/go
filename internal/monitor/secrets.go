package monitor

import (
	"bidmon/internal/checks"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"gorm.io/gorm/clause"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type StoredSecret struct {
	ID         string    `gorm:"primaryKey" json:"id"`
	Ciphertext []byte    `json:"-"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (StoredSecret) TableName() string { return "monitor_secrets" }

var secretID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func (r *GeneralRuntime) secretCipher() (cipher.AEAD, error) {
	path := os.Getenv("MON_SECRET_KEY_FILE")
	if path == "" {
		return nil, errors.New("secret_store_not_configured")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("secret_store_key_unavailable")
	}
	if len(key) != 32 {
		key, err = hex.DecodeString(strings.TrimSpace(string(key)))
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("secret_store_key_invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (r *GeneralRuntime) SaveSecret(id, value string) error {
	if !secretID.MatchString(id) || value == "" || len(value) > 65536 {
		return errors.New("invalid_secret")
	}
	aead, err := r.secretCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nonce, nonce, []byte(value), []byte(id))
	if err = r.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"ciphertext", "updated_at"})}).Create(&StoredSecret{ID: id, Ciphertext: sealed, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		return errors.New("secret_store_unavailable")
	}
	// Rotate connections on the next scan so an updated DSN takes effect.
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, runner := range r.runners {
		runner.Close()
	}
	r.runners = map[string]*checks.Runner{}
	return nil
}
func (r *GeneralRuntime) resolveSecret(ref checks.SecretRef) (string, error) {
	if !strings.HasPrefix(ref.File, "stored:") {
		return checks.ResolveSecret(ref)
	}
	id := strings.TrimPrefix(ref.File, "stored:")
	if !secretID.MatchString(id) || ref.Env != "" {
		return "", errors.New("invalid_secret_reference")
	}
	aead, err := r.secretCipher()
	if err != nil {
		return "", err
	}
	var row StoredSecret
	if r.DB.First(&row, "id=?", id).Error != nil {
		return "", errors.New("stored_secret_unavailable")
	}
	if len(row.Ciphertext) < aead.NonceSize() {
		return "", errors.New("stored_secret_invalid")
	}
	value, err := aead.Open(nil, row.Ciphertext[:aead.NonceSize()], row.Ciphertext[aead.NonceSize():], []byte(id))
	if err != nil {
		return "", errors.New("stored_secret_invalid")
	}
	return string(value), nil
}
