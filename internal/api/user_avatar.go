package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func userAvatarURL(u *db.User) string {
	if u.AvatarVersion == "" {
		return ""
	}
	return "/api/auth/me/avatar?v=" + u.AvatarVersion
}

// Decode and re-encode raster images to discard metadata and trailing content.
// Check dimensions before allocating pixels, even for a small compressed file.
func decodeUserAvatar(value string) ([]byte, string, error) {
	if value == "" {
		return nil, "", nil
	}
	prefix, encoded, ok := strings.Cut(value, ",")
	if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64") {
		return nil, "", errors.New("avatar must be a PNG or JPEG image")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(512<<10) {
		return nil, "", errors.New("avatar must be 512 KB or smaller")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", errors.New("invalid avatar image")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, "", errors.New("invalid avatar image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 512 || cfg.Height > 512 {
		return nil, "", errors.New("avatar dimensions must be at most 512 × 512 pixels")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("invalid avatar image")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, "", err
	}
	data = out.Bytes()
	return data, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (s *Server) handleGetOwnAvatar(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	data, err := s.store.GetUserAvatar(u.ID)
	if err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusNotFound, "no avatar")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// Empty restores the username-derived default. Only opaque RGB colors are
// accepted, so clients can consistently choose contrasting initials.
func normalizeAvatarColor(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) == 7 && value[0] == '#' {
		if _, err := strconv.ParseUint(value[1:], 16, 24); err == nil {
			return strings.ToLower(value), nil
		}
	}
	return "", errors.New("avatar color must be a six-digit hex color, such as #38bdf8")
}
