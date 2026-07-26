package app

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/monitor"
)

func TestMonitorRoutesKeepClusterPublicAndSessionAuthenticated(t *testing.T) {
	service := fakeMonitorService{}
	router, token, _ := newSessionMonitorTestAPI(t, &fakeSessionService{}, service)

	cluster := doJSON(t, router, http.MethodGet, "/api/v1/admin/cluster", "", nil)
	if cluster.Code != http.StatusOK {
		t.Fatalf("public cluster status=%d body=%s", cluster.Code, cluster.Body.String())
	}

	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")
	path := "/api/v1/sessions/" + sessionID.String() + "/resources"
	anonymous := doJSON(t, router, http.MethodGet, path, "", nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous session resources status=%d", anonymous.Code)
	}
	authenticated := doJSON(t, router, http.MethodGet, path, "", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if authenticated.Code != http.StatusOK {
		t.Fatalf("session resources status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}

	resize := doJSON(
		t,
		router,
		http.MethodPut,
		"/api/v1/admin/sessions/"+sessionID.String()+"/resources",
		`{"cpuMillicores":700,"memoryBytes":1073741824}`,
		nil,
	)
	if resize.Code != http.StatusAccepted {
		t.Fatalf("public resize status=%d body=%s", resize.Code, resize.Body.String())
	}
	restore := doJSON(
		t,
		router,
		http.MethodDelete,
		"/api/v1/admin/sessions/"+sessionID.String()+"/resources",
		"",
		nil,
	)
	if restore.Code != http.StatusAccepted {
		t.Fatalf("public restore status=%d body=%s", restore.Code, restore.Body.String())
	}
}

func TestManualResourceIntentOutsideTierBoundsReturnsUnprocessableEntity(t *testing.T) {
	service := fakeMonitorService{resizeErr: fmt.Errorf("%w: outside tier bounds", monitor.ErrInvalidResize)}
	router, _, _ := newSessionMonitorTestAPI(t, &fakeSessionService{}, service)
	sessionID := uuid.MustParse("018f9c6e-1234-7000-8000-abcdef012501")

	response := doJSON(
		t,
		router,
		http.MethodPut,
		"/api/v1/admin/sessions/"+sessionID.String()+"/resources",
		`{"cpuMillicores":1,"memoryBytes":1}`,
		nil,
	)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type fakeMonitorService struct {
	resizeErr error
}

func (fakeMonitorService) Session(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (monitor.SessionSnapshot, error) {
	return monitor.SessionSnapshot{
		ObservedAt:       time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
		MetricsAvailable: true,
	}, nil
}

func (fakeMonitorService) Cluster(context.Context) (monitor.ClusterSnapshot, error) {
	return monitor.ClusterSnapshot{
		ObservedAt:           time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
		PodMetricsAvailable:  true,
		NodeMetricsAvailable: true,
		Nodes:                []monitor.NodeSnapshot{},
		Pods:                 []monitor.PodSnapshot{},
	}, nil
}

func (f fakeMonitorService) ResizeAgent(
	context.Context,
	uuid.UUID,
	monitor.ResizeRequest,
) (monitor.ResourceScalingResponse, error) {
	if f.resizeErr != nil {
		return monitor.ResourceScalingResponse{}, f.resizeErr
	}
	return monitor.ResourceScalingResponse{
		ObservedAt: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (fakeMonitorService) RestoreAuto(
	context.Context,
	uuid.UUID,
) (monitor.ResourceScalingResponse, error) {
	return monitor.ResourceScalingResponse{
		ObservedAt: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}, nil
}
