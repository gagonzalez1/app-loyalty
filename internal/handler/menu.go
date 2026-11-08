package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func menuLanguage(c *gin.Context) (model.MenuLanguage, bool) {
	switch c.Query("lang") {
	case "", "es":
		return model.MenuSpanish, true
	case "en":
		return model.MenuEnglish, true
	default:
		writeErr(c, service.ErrInvalidRequest)
		return "", false
	}
}

func menuVersion(c *gin.Context) (int, bool) {
	if strings.TrimSpace(c.GetHeader("If-Match")) == "" {
		web.Error(c, http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", "Volvé a cargar el menú antes de modificarlo", nil)
		return 0, false
	}
	return accountVersion(c)
}
func menuIDs(c *gin.Context) (int64, int64, bool) {
	brand, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return 0, 0, false
	}
	branch, err := positiveID(c.Param("resource_id"))
	if err != nil {
		writeErr(c, err)
		return 0, 0, false
	}
	return brand, branch, true
}
func (h *Handler) privateMenuActor(c *gin.Context) (int64, int64, int64, bool) {
	a, ok := actor(c)
	if !ok {
		return 0, 0, 0, false
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return 0, 0, 0, false
	}
	brand, branch, ok := menuIDs(c)
	if !ok {
		return 0, 0, 0, false
	}
	if err := h.Repo.AuthorizeBranchMenu(c.Request.Context(), a.ID, brand, branch); err != nil {
		h.writeMenuError(c, err)
		return 0, 0, 0, false
	}
	return a.ID, brand, branch, true
}
func (h *Handler) writeMenuError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrMenuPDFTooLarge):
		web.Error(c, 413, "MENU_PDF_TOO_LARGE", "El PDF supera los 30 MiB", nil)
	case errors.Is(err, service.ErrMenuPDFType):
		web.Error(c, 415, "MENU_PDF_TYPE_UNSUPPORTED", "Elegí un archivo PDF", nil)
	case errors.Is(err, service.ErrMenuPDFInvalid):
		web.Error(c, 422, "MENU_PDF_INVALID", "El PDF está dañado o no se pudo validar", nil)
	case errors.Is(err, service.ErrMenuPDFPassword):
		web.Error(c, 422, "MENU_PDF_PASSWORD_PROTECTED", "El PDF debe estar sin contraseña ni cifrado", nil)
	case errors.Is(err, service.ErrMenuStorage):
		web.Error(c, 503, "MENU_STORAGE_UNAVAILABLE", "No pudimos guardar o abrir el PDF; reintentá", nil)
	default:
		if !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrForbidden) && !errors.Is(err, repository.ErrPreconditionFailed) && h.Logger != nil {
			h.Logger.ErrorContext(c.Request.Context(), "menu request failed", "request_id", web.RequestID(c), "route", c.FullPath())
		}
		writeErr(c, err)
	}
}
func menuResponse(c *gin.Context, status int, state model.BranchMenuState) {
	c.Header("Cache-Control", "no-store")
	c.Header("ETag", accountETag(state.Version))
	c.JSON(status, web.Envelope[model.BranchMenuState]{Data: state, RequestID: web.RequestID(c)})
}
func (h *Handler) BranchMenu(c *gin.Context) {
	a, b, branch, ok := h.privateMenuActor(c)
	if !ok {
		return
	}
	language, ok := menuLanguage(c)
	if !ok {
		return
	}
	state, err := h.Service.BranchMenu(c.Request.Context(), a, b, branch, language)
	if err != nil {
		h.writeMenuError(c, err)
		return
	}
	menuResponse(c, 200, state)
}
func (h *Handler) UploadMenuDocument(c *gin.Context) {
	a, b, branch, ok := h.privateMenuActor(c)
	if !ok {
		return
	}
	language, ok := menuLanguage(c)
	if !ok {
		return
	}
	if language == model.MenuEnglish {
		writeErr(c, repository.ErrForbidden)
		return
	}
	version, ok := menuVersion(c)
	if !ok {
		return
	}
	if !h.limit(c, fmt.Sprintf("media-upload:actor:%d", a), mediaActorAttempts, mediaUploadWindow) || !h.limit(c, "media-upload:ip:"+h.clientIP(c), mediaIPAttempts, mediaUploadWindow) {
		return
	}
	if h.Uploads == nil {
		h.writeMenuError(c, service.ErrMenuStorage)
		return
	}
	release, ok := h.Uploads.Acquire(c.Request.Context(), a)
	if !ok {
		h.writeMenuError(c, service.ErrMenuStorage)
		return
	}
	defer release()
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		h.writeMenuError(c, service.ErrMenuPDFType)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxMenuPDFBytes+(64<<10))
	if err := c.Request.ParseMultipartForm(64 << 10); err != nil {
		if c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeMenuError(c, service.ErrMenuPDFTooLarge)
		} else {
			writeErr(c, service.ErrInvalidRequest)
		}
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	if len(c.Request.MultipartForm.Value) != 0 || len(c.Request.MultipartForm.File) != 1 || len(c.Request.MultipartForm.File["archivo"]) != 1 {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	header := c.Request.MultipartForm.File["archivo"][0]
	if header.Size > service.MaxMenuPDFBytes {
		h.writeMenuError(c, service.ErrMenuPDFTooLarge)
		return
	}
	f, err := header.Open()
	if err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, service.MaxMenuPDFBytes+1))
	if err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	state, err := h.Service.UploadMenuDocument(c.Request.Context(), a, b, branch, language, version, header.Filename, body)
	if err != nil {
		h.writeMenuError(c, err)
		return
	}
	menuResponse(c, 201, state)
}
func (h *Handler) PublishBranchMenu(c *gin.Context)   { h.changeMenu(c, "publish") }
func (h *Handler) UnpublishBranchMenu(c *gin.Context) { h.changeMenu(c, "unpublish") }
func (h *Handler) DiscardMenuDraft(c *gin.Context)    { h.changeMenu(c, "discard") }
func (h *Handler) changeMenu(c *gin.Context, action string) {
	a, b, branch, ok := h.privateMenuActor(c)
	if !ok {
		return
	}
	language, ok := menuLanguage(c)
	if !ok {
		return
	}
	if language == model.MenuEnglish {
		writeErr(c, repository.ErrForbidden)
		return
	}
	version, ok := menuVersion(c)
	if !ok {
		return
	}
	document := ""
	if action == "publish" {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var req model.PublishMenuRequest
		if err := decode(c, &req); err != nil {
			writeErr(c, service.ErrInvalidRequest)
			return
		}
		document = req.DocumentID
	}
	state, err := h.Service.ChangeMenuPublication(c.Request.Context(), a, b, branch, language, version, action, document)
	if err != nil {
		h.writeMenuError(c, err)
		return
	}
	menuResponse(c, 200, state)
}
func (h *Handler) PublicBranchMenu(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	branch, err := positiveID(c.Param("branch_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	language, ok := menuLanguage(c)
	if !ok {
		return
	}
	data, err := h.Service.PublicBranchMenu(c.Request.Context(), branch, language)
	if err != nil {
		h.writeMenuError(c, err)
		return
	}
	c.JSON(200, web.Envelope[model.PublicBranchMenu]{Data: data, RequestID: web.RequestID(c)})
}
func (h *Handler) PublicBrandMenus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	brand, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	data, err := h.Service.PublicBrandMenus(c.Request.Context(), brand)
	if err != nil {
		h.writeMenuError(c, err)
		return
	}
	c.JSON(200, web.Envelope[[]model.PublicBrandMenu]{Data: data, RequestID: web.RequestID(c)})
}
