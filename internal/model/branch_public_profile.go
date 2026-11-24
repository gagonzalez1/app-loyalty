package model

// BranchPublicProfile contains only information a merchant chooses to publish.
type BranchPublicProfile struct {
	Hours []BranchHoursSlot `json:"horarios,omitempty"`
	Links map[string]string `json:"enlaces,omitempty"`
}

// Days use 0 for Monday through 6 for Sunday. A closing time before the
// opening time belongs to the following day.
type BranchHoursSlot struct {
	Days   []int  `json:"dias"`
	Opens  string `json:"abre"`
	Closes string `json:"cierra"`
}
