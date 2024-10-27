package server

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
	"github.com/rs/zerolog"
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

	r.Route("/api/v1/", func(r chi.Router) {
		r.Route("/healthz", func(r chi.Router) {
			r.Get("/liveness", func(w http.ResponseWriter, r *http.Request) {
				render.Status(r, http.StatusOK)
			})

			r.Get("/readiness", func(w http.ResponseWriter, r *http.Request) {
				//if err := s.service.Healthcheck(r.Context()); err != nil {
				//	render.Status(r, http.StatusFailedDependency)
				//	return
				//}

				render.Status(r, http.StatusOK)
			})
		})

		r.Group(func(r chi.Router) {
			// make sure request is authenticated
			r.Use(mw.IsAuthenticated(s.token))

			r.Route("/node", func(r chi.Router) {
				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					nodes := s.service.GetNodes()
					render.JSON(w, r, nodes)
				})

				r.Post("/register", func(w http.ResponseWriter, r *http.Request) {
					req := RegisterRequest{}

					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					if token := r.Context().Value("token").(string); token != "" {
						req.Token = token
					}

					if err := s.service.OnRegister(r.Context(), req); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					render.Status(r, http.StatusCreated)
					render.PlainText(w, r, "OK")
				})

				r.Post("/deregister", func(w http.ResponseWriter, r *http.Request) {
					req := DeregisterRequest{}

					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					if err := s.service.Deregister(r.Context(), req); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					render.Status(r, http.StatusOK)
					render.PlainText(w, r, "OK")
				})
			})

			r.Route("/tasks", func(r chi.Router) {
				r.Post("/", func(w http.ResponseWriter, r *http.Request) {
					//te := task.NewTask()
					te := task.NewEvent()
					//te.Task = t

					if err := json.NewDecoder(r.Body).Decode(&te.Task); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					ctx := context.WithoutCancel(r.Context())

					s.service.AddTask(ctx, te)

					render.Status(r, http.StatusCreated)
				})

				r.Get("/", func(w http.ResponseWriter, r *http.Request) {
					if err := getTasksHandler(r.Context()); err != nil {
						render.Status(r, http.StatusInternalServerError)
						return
					}

					render.PlainText(w, r, "get tasks")
					render.Status(r, http.StatusOK)
					return
				})
			})
		})
	})

	return r
}

func getTasksHandler(ctx context.Context) error {
	l := zerolog.Ctx(ctx)
	l.Info().Msg("get tasks")

	return nil
}
