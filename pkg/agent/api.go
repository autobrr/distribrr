package agent

import (
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
	host  string
	port  string
	token string

	service *Service
}

func NewAPIServer(cfg *Config, svc *Service) *APIServer {
	return &APIServer{
		host:    cfg.Http.Host,
		port:    cfg.Http.Port,
		token:   cfg.Http.Token,
		service: svc,
	}
}

func (s *APIServer) Open() error {
	addr := net.JoinHostPort(s.host, s.port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrapf(err, "could not open listener on: %s", addr)
	}

	server := http.Server{
		Handler: s.Handler(),
	}

	log.Info().Msgf("listening on: %s", listener.Addr())

	return server.Serve(listener)
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

					if err := s.service.StartTask(te.Task); err != nil {
						status := http.StatusInternalServerError
						if errors.Is(err, ErrInsufficientStorage) {
							status = http.StatusInsufficientStorage
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
