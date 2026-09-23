package handler

import (
	"github.com/gin-gonic/gin"
)

// Undo godoc
// @Summary Undo a move or swap adjustment
// @Description Re-validates the original positions against the latest timetable and teacher unavailable periods; rejects with 409 (without touching timetable or history) if occupied.
// @Tags schedules
// @Produce json
// @Param id path int true "adjustment log id"
// @Success 200 {object} github_com_gbschedule_gbschedule_internal_dto.Response
// @Failure 400 {object} github_com_gbschedule_gbschedule_internal_dto.Response
// @Failure 404 {object} github_com_gbschedule_gbschedule_internal_dto.Response
// @Failure 409 {object} github_com_gbschedule_gbschedule_internal_dto.Response
// @Router /api/v1/schedules/adjustments/{id}/undo [post]
func (h *ScheduleHandler) Undo(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	result, err := h.service.UndoAdjustment(c.Request.Context(), id)
	if err != nil {
		Error(c, err)
		return
	}
	OK(c, result)
}
