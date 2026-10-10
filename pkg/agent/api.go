package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"

	mw "github.com/autobrr/distribrr/pkg/middleware"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

type APIServer struct {
	token string

	service *Service
	server  *http.Server
}

func NewAPIServer(cfg *Config, svc *Service) *APIServer {
	s := &APIServer{
		token:   cfg.Http.Token,
		service: svc,
	}

	s.server = &http.Server{
		Handler: s.Handler(),
	}

	return s
}

// Serve serves the API on ln until Shutdown is called or serving fails. It
// closes ln when it returns, and returns nil after Shutdown.
func (s *APIServer) Serve(ln net.Listener) error {
	log.Info().Msgf("listening on: %s", ln.Addr())

	if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// Shutdown stops accepting new connections and waits for in-flight requests until ctx is done.
func (s *APIServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *APIServer) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(mw.CorrelationID)
	r.Use(mw.RequestLogger)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		render.PlainText(w, r, "OK")
		render.Status(r, http.StatusOK)
	})

	r.Route("/api/v1/", func(r chi.Router) {
		r.Route("/healthz", func(r chi.Router) {
			r.Get("/liveness", func(w http.ResponseWriter, r *http.Request) {
				render.PlainText(w, r, "OK")
				render.Status(r, http.StatusOK)
			})

			r.Get("/readiness", func(w http.ResponseWriter, r *http.Request) {
				if err := s.service.Healthcheck(r.Context()); err != nil {
					render.Status(r, http.StatusFailedDependency)
					return
				}

				render.PlainText(w, r, "OK")
				render.Status(r, http.StatusOK)
			})
		})

		r.Group(func(r chi.Router) {
			// make sure request is authenticated
			r.Use(mw.IsAuthenticated(s.token))

			r.Get("/verify", func(w http.ResponseWriter, r *http.Request) {
				render.Status(r, http.StatusOK)
			})

			r.Route("/tasks", func(r chi.Router) {
				r.Post("/", func(w http.ResponseWriter, r *http.Request) {
					te := task.Event{}

					if err := json.NewDecoder(r.Body).Decode(&te); err != nil {
						render.Status(r, http.StatusBadRequest)
						render.JSON(w, r, map[string]string{"error": "could not decode request body"})
						return
					}

					if err := s.service.StartTask(r.Context(), te.Task); err != nil {
						status := http.StatusInternalServerError
						switch {
						case errors.Is(err, ErrInsufficientStorage):
							status = http.StatusInsufficientStorage
						case errors.Is(err, ErrNoReadyClients):
							status = http.StatusServiceUnavailable
						}

						render.Status(r, status)
						render.JSON(w, r, map[string]string{"error": err.Error()})
						return
					}

					render.Status(r, http.StatusCreated)
					render.PlainText(w, r, "OK")
				})

				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					render.Status(r, http.StatusOK)
					render.PlainText(w, r, "OK")
				})
			})

			r.Route("/stats", func(r chi.Router) {
				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					s := s.service.GetStatsFull(r.Context())
					if s == nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					render.Status(r, http.StatusOK)
					render.JSON(w, r, s)
				})
			})

			r.Route("/labels", func(r chi.Router) {
				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					label := s.service.GetLabels()

					render.Status(r, http.StatusOK)
					render.JSON(w, r, label)
				})
			})
		})

	})

	return r
}
