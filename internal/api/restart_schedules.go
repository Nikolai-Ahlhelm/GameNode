package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/scheduler"
)

type restartScheduleInput struct {
	Enabled      *bool  `json:"enabled,omitempty"`
	ScheduleType string `json:"schedule_type"`
	TimeOfDay    string `json:"time_of_day"`
	DayOfWeek    *int   `json:"day_of_week,omitempty"`
	TimeZone     string `json:"time_zone"`
}

type restartSchedulePatch struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	ScheduleType *string `json:"schedule_type,omitempty"`
	TimeOfDay    *string `json:"time_of_day,omitempty"`
	DayOfWeek    **int   `json:"day_of_week,omitempty"`
	TimeZone     *string `json:"time_zone,omitempty"`
}

type restartScheduleResponse struct {
	scheduler.Schedule
	NextRestartAt *string `json:"next_restart_at,omitempty"`
}

func (s *Server) restartSchedulesHandler(w http.ResponseWriter, r *http.Request, serverID string, tail []string) {
	if s.restartSchedules == nil || len(tail) > 1 {
		notFound(w)
		return
	}
	if r.Method == http.MethodGet && len(tail) == 0 {
		if _, _, ok := s.requireServerPermission(w, r, "Server.View", serverID, false); !ok {
			return
		}
		schedules, err := s.restartSchedules.ListServer(r.Context(), serverID)
		if err != nil {
			internal(w)
			return
		}
		result := make([]restartScheduleResponse, 0, len(schedules))
		for _, schedule := range schedules {
			result = append(result, scheduleResponse(schedule))
		}
		jsonOut(w, http.StatusOK, map[string]any{"schedules": result})
		return
	}
	if len(tail) == 0 && r.Method == http.MethodPost {
		actor, _, ok := s.requireServerPermission(w, r, "Server.Edit", serverID, true)
		if !ok {
			return
		}
		var input restartScheduleInput
		if !decode(w, r, &input) {
			return
		}
		if _, err := s.servers.Get(r.Context(), serverID); err != nil {
			restartScheduleError(w, err)
			return
		}
		schedule := scheduler.Schedule{ServerID: serverID, Enabled: true, ScheduleType: strings.TrimSpace(input.ScheduleType), TimeOfDay: strings.TrimSpace(input.TimeOfDay), DayOfWeek: input.DayOfWeek, TimeZone: strings.TrimSpace(input.TimeZone)}
		if input.Enabled != nil {
			schedule.Enabled = *input.Enabled
		}
		created, err := s.restartSchedules.Create(r.Context(), schedule)
		if err != nil {
			s.recordRestartScheduleAudit(r, actor, audit.ServerRestartScheduleCreate, audit.Failure, serverID, "", schedule, err)
			restartScheduleError(w, err)
			return
		}
		s.syncRestartSchedule(r, created)
		s.recordRestartScheduleAudit(r, actor, audit.ServerRestartScheduleCreate, audit.Success, serverID, created.ID, created, nil)
		jsonOut(w, http.StatusCreated, scheduleResponse(created))
		return
	}
	if len(tail) != 1 {
		method(w)
		return
	}
	scheduleID := tail[0]
	if r.Method == http.MethodDelete {
		actor, _, ok := s.requireServerPermission(w, r, "Server.Edit", serverID, true)
		if !ok {
			return
		}
		current, err := s.restartSchedules.Get(r.Context(), scheduleID)
		if err != nil {
			restartScheduleError(w, err)
			return
		}
		if current.ServerID != serverID {
			notFound(w)
			return
		}
		if err = s.restartSchedules.Delete(r.Context(), scheduleID); err != nil {
			restartScheduleError(w, err)
			return
		}
		if s.restartScheduler != nil {
			s.restartScheduler.Remove(scheduleID)
		}
		s.recordRestartScheduleAudit(r, actor, audit.ServerRestartScheduleDelete, audit.Success, serverID, scheduleID, current, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	actor, _, ok := s.requireServerPermission(w, r, "Server.Edit", serverID, true)
	if !ok {
		return
	}
	current, err := s.restartSchedules.Get(r.Context(), scheduleID)
	if err != nil {
		restartScheduleError(w, err)
		return
	}
	if current.ServerID != serverID {
		notFound(w)
		return
	}
	var input restartSchedulePatch
	if !decode(w, r, &input) {
		return
	}
	updated, err := s.restartSchedules.Update(r.Context(), scheduleID, scheduler.Patch{Enabled: input.Enabled, ScheduleType: input.ScheduleType, TimeOfDay: input.TimeOfDay, DayOfWeek: input.DayOfWeek, TimeZone: input.TimeZone})
	if err != nil {
		s.recordRestartScheduleAudit(r, actor, audit.ServerRestartScheduleUpdate, audit.Failure, serverID, scheduleID, current, err)
		restartScheduleError(w, err)
		return
	}
	s.syncRestartSchedule(r, updated)
	action := audit.ServerRestartScheduleUpdate
	if current.Enabled != updated.Enabled {
		if updated.Enabled {
			action = audit.ServerRestartScheduleEnable
		} else {
			action = audit.ServerRestartScheduleDisable
		}
	}
	s.recordRestartScheduleAudit(r, actor, action, audit.Success, serverID, scheduleID, updated, nil)
	jsonOut(w, http.StatusOK, scheduleResponse(updated))
}

func (s *Server) syncRestartSchedule(r *http.Request, schedule scheduler.Schedule) {
	if s.restartScheduler == nil {
		return
	}
	if err := s.restartScheduler.Replace(r.Context(), schedule); err != nil {
		s.log.Warn("restart schedule runtime registration failed", "module", "RestartScheduler", "schedule_id", schedule.ID, "error", err)
	}
}

func scheduleResponse(schedule scheduler.Schedule) restartScheduleResponse {
	response := restartScheduleResponse{Schedule: schedule}
	if !schedule.Enabled {
		return response
	}
	if next, err := scheduler.NextOccurrence(timeNowUTC(), schedule); err == nil {
		value := next.Format(time.RFC3339Nano)
		response.NextRestartAt = &value
	}
	return response
}

var timeNowUTC = func() time.Time { return time.Now().UTC() }

func (s *Server) recordRestartScheduleAudit(r *http.Request, actor auth.User, action, result, serverID, scheduleID string, schedule scheduler.Schedule, err error) {
	metadata, _ := json.Marshal(map[string]any{"schedule_id": scheduleID, "schedule_type": schedule.ScheduleType, "time_of_day": schedule.TimeOfDay, "time_zone": schedule.TimeZone})
	in := auditInput{action: action, resourceType: audit.Server, resourceID: stringPointer(scheduleID), serverID: stringPointer(serverID), result: result, actor: &actor, metadata: metadata}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
		in.err = err
	}
	s.recordAudit(r, in)
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func restartScheduleError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if errors.Is(err, scheduler.ErrInvalidSchedule) {
		bad(w, err.Error())
		return
	}
	internal(w)
}
