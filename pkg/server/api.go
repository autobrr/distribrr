package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"

	mw "github.com/autobrr/distribrr/pkg/middleware"
	"github.com/autobrr/distribrr/pkg/scheduler"
	"github.com/autobrr/distribrr/pkg/sharedhttp"
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

	s.server = sharedhttp.NewServer(s.Handler())

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
	})

	r.Route("/api/v1/", func(r chi.Router) {
		r.Route("/healthz", func(r chi.Router) {
			r.Get("/liveness", func(w http.ResponseWriter, r *http.Request) {
				render.PlainText(w, r, "OK")
			})

			r.Get("/readiness", func(w http.ResponseWriter, r *http.Request) {
				render.PlainText(w, r, "OK")
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

				// agents poll this to find out whether the server still knows them
				r.Get("/{name}", func(w http.ResponseWriter, r *http.Request) {
					n, err := s.service.GetNode(chi.URLParam(r, "name"))
					if err != nil {
						render.Status(r, http.StatusNotFound)
						render.JSON(w, r, map[string]string{"error": err.Error()})
						return
					}

					render.JSON(w, r, n)
				})

				r.Post("/register", func(w http.ResponseWriter, r *http.Request) {
					req := RegisterRequest{}

					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						render.Status(r, http.StatusBadRequest)
						render.JSON(w, r, map[string]string{"error": "could not decode request body"})
						return
					}

					if token := r.Context().Value("token").(string); token != "" {
						req.ServerToken = token
					}

					if err := s.service.OnRegister(r.Context(), req); err != nil {
						render.Status(r, http.StatusInternalServerError)
						render.JSON(w, r, map[string]string{"error": err.Error()})
						return
					}

					render.Status(r, http.StatusCreated)
					render.PlainText(w, r, "OK")
				})

				r.Post("/deregister", func(w http.ResponseWriter, r *http.Request) {
					req := DeregisterRequest{}

					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						render.Status(r, http.StatusBadRequest)
						render.JSON(w, r, map[string]string{"error": "could not decode request body"})
						return
					}

					if err := s.service.Deregister(r.Context(), req); err != nil {
						status := http.StatusInternalServerError
						if errors.Is(err, ErrNodeNotFound) {
							status = http.StatusNotFound
						}
						render.Status(r, status)
						render.JSON(w, r, map[string]string{"error": err.Error()})
						return
					}

					render.Status(r, http.StatusOK)
					render.PlainText(w, r, "OK")
				})
			})

			r.Route("/tasks", func(r chi.Router) {
				r.Post("/", func(w http.ResponseWriter, r *http.Request) {
					te := task.NewEvent()

					if err := json.NewDecoder(r.Body).Decode(&te.Task); err != nil {
						render.Status(r, http.StatusBadRequest)
						render.JSON(w, r, map[string]string{"error": "could not decode request body"})
						return
					}

					// detach from the request so dispatch isn't cancelled if the
					// caller disconnects, while still carrying request values.
					ctx := context.WithoutCancel(r.Context())

					if err := s.service.AddTask(ctx, te); err != nil {
						status := http.StatusBadGateway
						if errors.Is(err, scheduler.ErrUnknownType) {
							status = http.StatusBadRequest
						}
						render.Status(r, status)
						render.JSON(w, r, map[string]string{"error": err.Error()})
						return
					}

					render.Status(r, http.StatusCreated)
					render.PlainText(w, r, "OK")
				})
			})
		})
	})

	return r
}
