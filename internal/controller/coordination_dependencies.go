package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/przemekhys/homeassistant-operator/internal/coordination"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// CoordinationDependencies keeps direct reads and the bounded restore session
// runner explicit in reconcilers. Concrete operation reconcilers consume this
// struct when they are registered.
type CoordinationDependencies struct {
	APIReader       client.Reader
	Store           coordination.Store
	RestoreSessions *haclient.RestoreSessionRunner
}

func NewCoordinationDependencies(
	apiReader client.Reader,
	writer client.Client,
	sessions *haclient.RestoreSessionRunner,
) CoordinationDependencies {
	return CoordinationDependencies{
		APIReader:       apiReader,
		Store:           coordination.Store{Reader: apiReader, Client: writer},
		RestoreSessions: sessions,
	}
}
