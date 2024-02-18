// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package main

import (
	"fmt"
	"github.com/b4o4/conference-backend/internal/routes"
	"github.com/joho/godotenv"
	"log"
	"net/http"
	"os"
)

// nolint
var (
	port string
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Print("No .env file found")
	}

	envPort, exist := os.LookupEnv("PORT")

	if !exist {
		log.Fatal("PORT not write in .env")
	} else {
		port = envPort
	}
}

func main() {
	router := routes.NewRouter()

	// start HTTP server
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), router)) // nolint:gosec
}
