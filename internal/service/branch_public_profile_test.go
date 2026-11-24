package service

import (
	"errors"
	"testing"

	"clientesFrecuentes/internal/model"
)

func TestBranchPublicProfileValidation(t *testing.T) {
	slot := func(day int, opens, closes string) model.BranchHoursSlot {
		return model.BranchHoursSlot{Days: []int{day}, Opens: opens, Closes: closes}
	}
	for _, tc := range []struct {
		name    string
		profile *model.BranchPublicProfile
		wantErr bool
	}{
		{name: "optional"},
		{name: "split and overnight shifts", profile: &model.BranchPublicProfile{
			Hours: []model.BranchHoursSlot{slot(6, "21:00", "01:00"), slot(0, "01:00", "02:00"), slot(0, "12:00", "15:00")},
			Links: map[string]string{"menu_ingles": "https://example.com/menu.pdf", "instagram": "https://www.instagram.com/puntazo"},
		}},
		{name: "overnight overlap across week", profile: &model.BranchPublicProfile{Hours: []model.BranchHoursSlot{slot(6, "21:00", "01:00"), slot(0, "00:30", "02:00")}}, wantErr: true},
		{name: "invalid day", profile: &model.BranchPublicProfile{Hours: []model.BranchHoursSlot{slot(7, "10:00", "13:00")}}, wantErr: true},
		{name: "zero duration", profile: &model.BranchPublicProfile{Hours: []model.BranchHoursSlot{slot(0, "10:00", "10:00")}}, wantErr: true},
		{name: "incorrect platform", profile: &model.BranchPublicProfile{Links: map[string]string{"instagram": "https://instagram.com.evil.example/profile"}}, wantErr: true},
		{name: "non HTTPS URL", profile: &model.BranchPublicProfile{Links: map[string]string{"sitio_web": "javascript:alert(1)"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cleanBranchPublicProfile(tc.profile)
			if (tc.wantErr && !errors.Is(err, ErrInvalidRequest)) || (!tc.wantErr && err != nil) {
				t.Fatalf("validation error = %v, want error = %v", err, tc.wantErr)
			}
		})
	}
}
