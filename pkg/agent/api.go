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
		log.Error().Err(err).Msgf("could not open listener on: %s", addr)
		//return errors.Wrap(err, "error opening http server")
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
						render.Status(r, http.StatusInternalServerError)
						return
					}

					//s.service.AddTask(te.Task)
					if err := s.service.StartTask(te.Task); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					render.Status(r, http.StatusCreated)
				})

				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					render.Status(r, http.StatusOK)
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
