package handler

import "github.com/openvibely/openvibely/internal/applog"

// SetLocalServicesGate is configured before listeners or request handlers start.
func (h *Handler) SetLocalServicesGate(allowed func() bool) { h.localServicesAllowed = allowed }

// StartDynamicLocalChannels uses current instances, including channels configured
// after startup. Fixed channel instances are managed by the server controller.
func (h *Handler) StartDynamicLocalChannels() {
	h.telegramConfigMu.Lock()
	if h.telegramService != nil {
		h.telegramService.Start()
	}
	h.telegramConfigMu.Unlock()
	h.xConfigMu.Lock()
	defer h.xConfigMu.Unlock()
	if svc := h.getXService(); svc != nil && svc.Status().Configured {
		if err := svc.Start(); err != nil {
			applog.Infof("warning: failed to start X polling: %v", err)
		} else if h.channelMessageRouter != nil {
			h.channelMessageRouter.SetXService(svc)
		}
	}
}

func (h *Handler) StopDynamicLocalChannels() {
	h.telegramConfigMu.Lock()
	if h.telegramService != nil {
		h.telegramService.StopAndWait()
	}
	h.telegramConfigMu.Unlock()
	h.xConfigMu.Lock()
	defer h.xConfigMu.Unlock()
	if svc := h.getXService(); svc != nil {
		svc.Stop()
	}
}
