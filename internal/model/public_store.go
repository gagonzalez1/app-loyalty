package model

type PublicStore struct {
	BranchID      int64                `json:"branch_id"`
	BrandName     string               `json:"brand_name"`
	BranchName    string               `json:"branch_name"`
	Address       *string              `json:"address,omitempty"`
	Locality      *string              `json:"localidad,omitempty"`
	Province      *string              `json:"provincia,omitempty"`
	PublicProfile *BranchPublicProfile `json:"perfil_publico,omitempty"`
}
