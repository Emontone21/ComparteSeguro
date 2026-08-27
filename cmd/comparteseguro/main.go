// Comando comparteseguro levanta la aplicación web Comparte Seguro.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Emontone21/ComparteSeguro/internal/almacen"
	"github.com/Emontone21/ComparteSeguro/internal/config"
	"github.com/Emontone21/ComparteSeguro/internal/servidor"
)

func main() {
	if err := ejecutar(); err != nil {
		slog.Error("la aplicación no pudo arrancar", "error", err)
		os.Exit(1)
	}
}

func ejecutar() error {
	cfg, err := config.Cargar()
	if err != nil {
		return err
	}

	log := construirLog(cfg.FormatoLog)
	slog.SetDefault(log)

	// La configuración se registra entera porque no contiene ningún secreto:
	// esta aplicación no tiene claves ni credenciales que guardar.
	log.Info("iniciando Comparte Seguro",
		"direccion", cfg.DireccionDeEscucha(),
		"base_de_datos", cfg.RutaDB,
		"max_bytes_nota", cfg.MaxBytesNota,
		"peticiones_por_minuto", cfg.PeticionesPorMinuto,
		"rafaga", cfg.Rafaga,
		"confiar_en_proxy", cfg.ConfiarEnProxy,
	)

	if dir := filepath.Dir(cfg.RutaDB); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}

	alm, err := almacen.Abrir(cfg.RutaDB)
	if err != nil {
		return err
	}
	defer alm.Cerrar()

	app, err := servidor.Nuevo(servidor.Opciones{
		Almacen:             alm,
		Log:                 log,
		MaxBytesNota:        cfg.MaxBytesNota,
		PeticionesPorMinuto: cfg.PeticionesPorMinuto,
		Rafaga:              cfg.Rafaga,
		ConfiarEnProxy:      cfg.ConfiarEnProxy,
	})
	if err != nil {
		return err
	}

	// El contexto se cancela con SIGINT o SIGTERM: es lo que manda Docker al
	// detener el contenedor.
	ctx, detener := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer detener()

	app.IniciarTareas(ctx)

	srv := &http.Server{
		Addr:    cfg.DireccionDeEscucha(),
		Handler: app,
		// Plazos explícitos para que una conexión lenta o abandonada no se
		// quede colgada ocupando recursos.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	fallo := make(chan error, 1)
	go func() {
		log.Info("escuchando", "direccion", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fallo <- err
		}
	}()

	select {
	case err := <-fallo:
		return err
	case <-ctx.Done():
		log.Info("apagando de forma ordenada")
	}

	// Darle tiempo a las peticiones en curso a terminar antes de cortar.
	ctxApagado, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()
	if err := srv.Shutdown(ctxApagado); err != nil {
		return err
	}
	log.Info("apagado completo")
	return nil
}

func construirLog(formato string) *slog.Logger {
	opciones := &slog.HandlerOptions{Level: slog.LevelInfo}
	if formato == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opciones))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opciones))
}
