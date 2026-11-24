package model

// StoreLocation contains public merchant information only, never loyalty balances.
type StoreLocation struct {
	BrandID        int64                  `json:"brand_id"`
	BrandName      string                 `json:"brand_name"`
	BranchID       int64                  `json:"branch_id"`
	BranchName     string                 `json:"branch_name"`
	Address        *string                `json:"address,omitempty"`
	Locality       *string                `json:"localidad,omitempty"`
	Province       *string                `json:"provincia,omitempty"`
	Latitude       *float64               `json:"latitud,omitempty"`
	Longitude      *float64               `json:"longitud,omitempty"`
	Description    *string                `json:"descripcion,omitempty"`
	Category       string                 `json:"categoria,omitempty"`
	Phone          string                 `json:"telefono,omitempty"`
	PublicProfile  *BranchPublicProfile   `json:"perfil_publico,omitempty"`
	HasEnglishMenu bool                   `json:"has_english_menu"`
	HasMenu        bool                   `json:"has_menu"`
	OpeningHours   string                 `json:"horarios,omitempty"`
	ImageURL       string                 `json:"image_url,omitempty"`
	LogoObjectKey  string                 `json:"-"`
	Benefits       []StoreLocationBenefit `json:"beneficios"`
	Favorite       bool                   `json:"favorita"`
	Favorites      int64                  `json:"favoritos"`
}

type StoreLocationBenefit struct {
	ID             int64  `json:"id"`
	Name           string `json:"nombre"`
	Description    string `json:"descripcion,omitempty"`
	RequiredStamps *int64 `json:"required_stamps,omitempty"`
	RequiredPoints *int64 `json:"required_points,omitempty"`
}

type StoreFavorite struct {
	BranchID  int64 `json:"branch_id"`
	Favorite  bool  `json:"favorita"`
	Favorites int64 `json:"favoritos"`
}

// StoreGoogleReview exposes only the configured public destination, not its
// private invitation settings or the customer's purchase history.
type StoreGoogleReview struct {
	BranchID  int64   `json:"branch_id"`
	ReviewURL *string `json:"review_url"`
}
