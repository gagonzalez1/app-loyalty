package service

import (
	"context"
	"regexp"
	"strings"

	"clientesFrecuentes/internal/model"
)

var (
	hexColor   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	rewardIcon = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
)

var cardTemplates = map[string]bool{
	"COMIC_HQ": true, "MINIMAL_PRO": true, "POP_BADGE": true, "PREMIUM_GOLD_CHECKS": true, "DARK_LUXURY_CHECKS": true,
	"PREMIUM_GOLD": true, "DARK_LUXURY": true, "POP_HEADER": true, "COMIC_HQ_POINTS": true, "MINIMAL_PRO_POINTS": true,
	"POP_BADGE_POINTS": true, "NEON_PULSE": true, "SUNSET_GRADIENT": true,
}

func (s *Service) UpdateBrand(ctx context.Context, a, b int64, v int, r model.UpdateBrandRequest) (model.MerchantContext, error) {
	if r.Name == nil && r.Description == nil && r.PrimaryColor == nil && r.SecondaryColor == nil && r.Timezone == nil && r.CardTemplate == nil && r.RewardImage == nil {
		return model.MerchantContext{}, ErrInvalidRequest
	}
	for _, x := range []*string{r.Name, r.Description, r.PrimaryColor, r.SecondaryColor, r.Timezone, r.CardTemplate, r.RewardImage} {
		if x != nil {
			*x = strings.TrimSpace(*x)
		}
	}
	if r.Name != nil && (*r.Name == "" || len(*r.Name) > 120) || r.Description != nil && len(*r.Description) > 1000 || r.PrimaryColor != nil && *r.PrimaryColor != "" && !hexColor.MatchString(*r.PrimaryColor) || r.SecondaryColor != nil && *r.SecondaryColor != "" && !hexColor.MatchString(*r.SecondaryColor) || r.Timezone != nil && *r.Timezone == "" || r.CardTemplate != nil && !cardTemplates[*r.CardTemplate] || r.RewardImage != nil && !rewardIcon.MatchString(*r.RewardImage) {
		return model.MerchantContext{}, ErrInvalidRequest
	}
	return s.Repo.UpdateBrand(ctx, a, b, v, r)
}
func (s *Service) DeleteBrand(c context.Context, a, b int64, v int) error {
	return s.Repo.DeleteBrand(c, a, b, v)
}
func (s *Service) Branches(c context.Context, a, b int64) ([]model.Branch, error) {
	return s.Repo.ListBranches(c, a, b)
}
func (s *Service) Branch(c context.Context, a, b, id int64) (model.Branch, error) {
	return s.Repo.GetBranch(c, a, b, id)
}
func cleanBranch(r *model.CreateBranchRequest) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 120 || r.Latitude != nil && (*r.Latitude < -90 || *r.Latitude > 90) || r.Longitude != nil && (*r.Longitude < -180 || *r.Longitude > 180) {
		return ErrInvalidRequest
	}
	return nil
}
func (s *Service) CreateBranch(c context.Context, a, b int64, r model.CreateBranchRequest) (model.Branch, error) {
	if err := cleanBranch(&r); err != nil {
		return model.Branch{}, err
	}
	return s.Repo.CreateBranch(c, a, b, r)
}
func (s *Service) UpdateBranch(c context.Context, a, b, id int64, v int, r model.UpdateBranchRequest) (model.Branch, error) {
	base := model.CreateBranchRequest{Name: r.Name, Address: r.Address, Locality: r.Locality, Province: r.Province, PostalCode: r.PostalCode, Latitude: r.Latitude, Longitude: r.Longitude}
	if err := cleanBranch(&base); err != nil {
		return model.Branch{}, err
	}
	r.Name = base.Name
	return s.Repo.UpdateBranch(c, a, b, id, v, r)
}
func (s *Service) DeleteBranch(c context.Context, a, b, id int64, v int) error {
	return s.Repo.DeleteBranch(c, a, b, id, v)
}
func (s *Service) Program(c context.Context, a, b int64) (model.Program, error) {
	return s.Repo.GetProgram(c, a, b)
}
func (s *Service) UpdateProgram(c context.Context, a, b int64, v int, r model.UpdateProgramRequest) (model.Program, error) {
	r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
	r.UnitName = strings.TrimSpace(r.UnitName)
	if (r.Type != "SELLOS" && r.Type != "PUNTOS") || r.UnitName == "" || len(r.UnitName) > 40 {
		return model.Program{}, ErrInvalidRequest
	}
	return s.Repo.UpdateProgram(c, a, b, v, r)
}
func (s *Service) Benefit(c context.Context, a, b, id int64) (model.Benefit, error) {
	return s.Repo.GetBenefit(c, a, b, id)
}
func validateBenefit(name, description string, requirement int64) error {
	if strings.TrimSpace(name) == "" || len(name) > 120 || len(description) > 1000 || requirement < 1 || requirement > 10000000 {
		return ErrInvalidRequest
	}
	return nil
}
func (s *Service) ReplaceBenefit(c context.Context, a, b, id int64, v int, r model.ReplaceBenefitRequest) (model.Benefit, error) {
	r.Name = strings.TrimSpace(r.Name)
	r.Description = strings.TrimSpace(r.Description)
	if err := validateBenefit(r.Name, r.Description, r.Requirement); err != nil {
		return model.Benefit{}, err
	}
	return s.Repo.ReplaceBenefit(c, a, b, id, v, r)
}
func (s *Service) UpdateBenefit(c context.Context, a, b, id int64, v int, r model.UpdateBenefitRequest) (model.Benefit, error) {
	cur, err := s.Repo.GetBenefit(c, a, b, id)
	if err != nil {
		return cur, err
	}
	rep := model.ReplaceBenefitRequest{Name: cur.Name, Description: cur.Description, Active: cur.Active}
	if cur.RequiredPoints != nil {
		rep.Requirement = *cur.RequiredPoints
	} else if cur.RequiredStamps != nil {
		rep.Requirement = *cur.RequiredStamps
	}
	if r.Name != nil {
		rep.Name = *r.Name
	}
	if r.Description != nil {
		rep.Description = *r.Description
	}
	if r.Requirement != nil {
		rep.Requirement = *r.Requirement
	}
	if r.Active != nil {
		rep.Active = *r.Active
	}
	return s.ReplaceBenefit(c, a, b, id, v, rep)
}
func (s *Service) DeleteBenefit(c context.Context, a, b, id int64, v int) error {
	return s.Repo.DeleteBenefit(c, a, b, id, v)
}
