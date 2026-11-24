package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const MaxMenuPDFBytes int64 = config.MaxMenuPDFBytes
const menuURLMaxTTL = 300 * time.Second

var (
	ErrMenuPDFTooLarge = errors.New("menu PDF exceeds 30 MiB")
	ErrMenuPDFType     = errors.New("menu PDF type unsupported")
	ErrMenuPDFInvalid  = errors.New("menu PDF invalid")
	ErrMenuPDFPassword = errors.New("menu PDF encrypted")
	ErrMenuStorage     = errors.New("menu storage unavailable")
)

func validateMenuPDF(ctx context.Context, body []byte) (int, []byte, error) {
	if int64(len(body)) > MaxMenuPDFBytes {
		return 0, nil, ErrMenuPDFTooLarge
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		return 0, nil, ErrMenuPDFType
	}
	parseCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		return 0, nil, ErrMenuPDFInvalid
	}
	// Accept metadata conventions used by PDF exporters. Parser resource limits
	// and the encryption and page-count checks still apply.
	conf.ValidationMode = pdfmodel.ValidationRelaxed
	conf.ValidateLinks = false
	conf.DecodeAllStreams = false
	conf.Cmd = pdfmodel.VALIDATE
	conf.Limits = pdfmodel.ResourceLimits{MaxInputBytes: MaxMenuPDFBytes, MaxObjectBytes: 2 << 20, MaxStreamBytes: MaxMenuPDFBytes, MaxDecodeBytes: 32 << 20, MaxImagePixels: 25_000_000, MaxImageBytes: 64 << 20, MaxObjectCount: 100000, MaxObjectStreamCount: 100000, MaxObjectStreamFirst: 2 << 20, MaxXRefEntries: 100000, MaxRecursionDepth: 50}
	pdf, err := api.ReadContext(parseCtx, bytes.NewReader(body), conf)
	// Encryption metadata can fail before password authentication, for example
	// PyPDF's unsigned permission flags. These structured errors still identify
	// protection; keep rejecting the file and explain that encryption must go.
	if errors.Is(err, pdfcpu.ErrEncrypted) || errors.Is(err, pdfcpu.ErrWrongPassword) ||
		errors.Is(err, pdfcpu.ErrMalformedEncryption) || errors.Is(err, pdfcpu.ErrUnsupportedEncryptionFeature) {
		return 0, nil, ErrMenuPDFPassword
	}
	if err != nil {
		return 0, nil, ErrMenuPDFInvalid
	}
	if pdf.Encrypt != nil {
		return 0, nil, ErrMenuPDFPassword
	}
	if err = api.ValidateContext(parseCtx, pdf); err != nil {
		return 0, nil, ErrMenuPDFInvalid
	}
	if pdf.PageCount < 1 {
		return 0, nil, ErrMenuPDFInvalid
	}
	sum := sha256.Sum256(body)
	return pdf.PageCount, sum[:], nil
}
func menuFileName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	name = strings.TrimSpace(name)
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	if len(name) > 160 {
		base := strings.TrimSuffix(name, name[len(name)-4:])
		for len(base) > 156 {
			_, n := utf8.DecodeLastRuneInString(base)
			base = base[:len(base)-n]
		}
		name = base + ".pdf"
	}
	if name == ".pdf" {
		return "menu.pdf"
	}
	return name
}
func (s *Service) signMenuDocument(ctx context.Context, d *model.MenuDocument) error {
	if d == nil {
		return nil
	}
	if s.Media == nil {
		return ErrMenuStorage
	}
	ttl := s.Config.MediaURLTTL
	if ttl <= 0 || ttl > menuURLMaxTTL {
		ttl = menuURLMaxTTL
	}
	url, err := s.Media.SignedGet(ctx, d.ObjectKey, ttl)
	if err != nil || !strings.HasPrefix(url, "https://") {
		return ErrMenuStorage
	}
	d.URL = url
	d.URLExpiresAt = s.Now().Add(ttl)
	return nil
}
func (s *Service) signMenuState(ctx context.Context, state model.BranchMenuState) (model.BranchMenuState, error) {
	if err := s.signMenuDocument(ctx, state.Draft); err != nil {
		return model.BranchMenuState{}, err
	}
	if err := s.signMenuDocument(ctx, state.Published); err != nil {
		return model.BranchMenuState{}, err
	}
	return state, nil
}
func (s *Service) BranchMenu(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage) (model.BranchMenuState, error) {
	return s.Repo.BranchMenu(ctx, actor, brand, branch, language, s.signMenuState)
}
func (s *Service) UploadMenuDocument(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, version int, name string, body []byte) (model.BranchMenuState, error) {
	if err := s.Repo.AuthorizeBranchMenu(ctx, actor, brand, branch); err != nil {
		return model.BranchMenuState{}, err
	}
	if s.Media == nil {
		return model.BranchMenuState{}, ErrMenuStorage
	}
	pages, digest, err := validateMenuPDF(ctx, body)
	if err != nil {
		return model.BranchMenuState{}, err
	}
	id := uuid.NewString()
	d := model.MenuDocument{ID: id, FileName: menuFileName(name), MIMEType: "application/pdf", ByteSize: int64(len(body)), PageCount: pages, ObjectKey: "menus/" + id + ".pdf"}
	if err = s.Repo.ReserveMenuDocument(ctx, actor, brand, branch, language, version, d, digest); err != nil {
		return model.BranchMenuState{}, err
	}
	if err = s.Media.Put(ctx, d.ObjectKey, d.MIMEType, body, digest); err != nil {
		compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Repo.FailMenuUpload(compensation, id)
		return model.BranchMenuState{}, ErrMenuStorage
	}
	out, err := s.Repo.ActivateMenuDraft(ctx, actor, brand, branch, language, version, id, s.signMenuState)
	if err != nil {
		compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Repo.FailMenuUpload(compensation, id)
	}
	return out, err
}
func (s *Service) ChangeMenuPublication(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, version int, action, document string) (model.BranchMenuState, error) {
	if action == "publish" {
		parsed, err := uuid.Parse(document)
		if err != nil || parsed == uuid.Nil {
			return model.BranchMenuState{}, ErrInvalidRequest
		}
		document = parsed.String()
	}
	return s.Repo.ChangeMenuPublication(ctx, actor, brand, branch, language, version, action, document, s.signMenuState)
}
func (s *Service) PublicBranchMenu(ctx context.Context, branch int64, language model.MenuLanguage) (model.PublicBranchMenu, error) {
	return s.Repo.PublicBranchMenu(ctx, branch, language, s.signMenuDocument)
}
func (s *Service) PublicBrandMenus(ctx context.Context, brand int64) ([]model.PublicBrandMenu, error) {
	return s.Repo.PublicBrandMenus(ctx, brand)
}
