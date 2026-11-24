package model

import "time"

type MenuLanguage string

const (
	MenuSpanish MenuLanguage = "es"
	MenuEnglish MenuLanguage = "en"
)

type MenuDocument struct {
	ID           string    `json:"id"`
	FileName     string    `json:"file_name"`
	MIMEType     string    `json:"mime_type"`
	ByteSize     int64     `json:"byte_size"`
	PageCount    int       `json:"page_count"`
	CreatedAt    time.Time `json:"created_at"`
	URL          string    `json:"url"`
	URLExpiresAt time.Time `json:"url_expires_at"`
	ObjectKey    string    `json:"-"`
}

type BranchMenuState struct {
	BrandID   int64         `json:"brand_id"`
	BranchID  int64         `json:"branch_id"`
	Version   int           `json:"version"`
	Draft     *MenuDocument `json:"draft"`
	Published *MenuDocument `json:"published"`
}

type PublicBranchMenu struct {
	BrandID    int64         `json:"brand_id"`
	BranchID   int64         `json:"branch_id"`
	BrandName  string        `json:"brand_name"`
	BranchName string        `json:"branch_name"`
	Published  *MenuDocument `json:"published"`
}

type PublicBrandMenu struct {
	BrandID    int64    `json:"brand_id"`
	BranchID   int64    `json:"branch_id"`
	BrandName  string   `json:"brand_name"`
	BranchName string   `json:"branch_name"`
	Address    *string  `json:"address"`
	Latitude   *float64 `json:"latitud"`
	Longitude  *float64 `json:"longitud"`
	HasMenu    bool     `json:"has_menu"`
}

type PublishMenuRequest struct {
	DocumentID string `json:"document_id"`
}
