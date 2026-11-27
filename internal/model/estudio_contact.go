package model

type EstudioContact struct {
	BranchID      int64                `json:"branch_id"`
	Version       int                  `json:"version"`
	Address       *string              `json:"address"`
	PublicProfile *BranchPublicProfile `json:"perfil_publico,omitempty"`
	CanWrite      bool                 `json:"can_write"`
}

type EstudioContactPatch struct {
	Address   *string            `json:"address"`
	WhatsApp  *string            `json:"whatsapp"`
	Instagram *string            `json:"instagram"`
	TikTok    *string            `json:"tiktok"`
	Hours     *[]BranchHoursSlot `json:"horarios"`
}
