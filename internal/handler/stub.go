package handler

import (
	"net/http"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

func notImplemented(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, apperror.New(apperror.CodeNotImplemented, apperror.MsgNotImplemented))
}
