package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ProfilePhoto(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	photo, err := h.Service.ProfilePhoto(c.Request.Context(), a.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.ProfilePhoto]{Data: photo, RequestID: web.RequestID(c)})
}

func (h *Handler) UploadProfilePhoto(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	version, ok := accountVersion(c)
	if !ok {
		return
	}
	if !h.limit(c, fmt.Sprintf("profile-photo-upload:actor:%d", a.ID), mediaActorAttempts, mediaUploadWindow) ||
		!h.limit(c, "profile-photo-upload:ip:"+h.clientIP(c), mediaIPAttempts, mediaUploadWindow) {
		return
	}
	if h.Uploads == nil {
		writeErr(c, service.ErrMediaUnavailable)
		return
	}
	release, acquired := h.Uploads.Acquire(c.Request.Context(), a.ID)
	if !acquired {
		writeErr(c, service.ErrMediaUnavailable)
		return
	}
	defer release()
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		writeErr(c, service.ErrMediaType)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, (5<<20)+(64<<10))
	if err := c.Request.ParseMultipartForm(64 << 10); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(c, service.ErrMediaTooLarge)
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
	file, err := c.Request.MultipartForm.File["archivo"][0].Open()
	if err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if len(body) > 5<<20 {
		writeErr(c, service.ErrMediaTooLarge)
		return
	}
	updated, err := h.Service.UploadProfilePhoto(c.Request.Context(), a.ID, version, body)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", accountETag(updated.Current.User.Version))
	c.JSON(http.StatusOK, web.Envelope[model.ProfilePhotoUpdate]{Data: updated, RequestID: web.RequestID(c)})
}
