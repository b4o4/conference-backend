package routes

import (
	"fmt"
	"github.com/b4o4/conference-backend/internal/websockets"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"log"
	"net/http"
	"os"
	"strings"
	"text/template"
)

var (
	host          string
	websocketType string
	path          string
	indexTemplate = &template.Template{}
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Print("No .env file found")
	}

	envHost, exist := os.LookupEnv("HOST")

	if !exist {
		log.Fatal("HOST not write in .env")
	} else {
		host = envHost
	}

	envSchema, exist := os.LookupEnv("SCHEMA")

	if !exist {
		log.Fatal("SCHEMA not write in .env")
	} else {
		if strings.ToLower(envSchema) == "https" {
			websocketType = "wss://"
		} else {
			websocketType = "ws://"
		}
	}

	pwd, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	path = pwd

}

func NewRouter() http.Handler {
	router := mux.NewRouter()

	router.HandleFunc("/room/{uuid}", indexHandler)
	router.HandleFunc("/websocket/{uuid}/join", websockets.Handler)
	router.HandleFunc("/", conferenceHandler)
	router.HandleFunc("/conference/create", createConferenceHandler)

	return router
}

func conferenceHandler(w http.ResponseWriter, r *http.Request) {

	lobbyHTML, _ := os.ReadFile(path + "/templates/lobby.html")

	tmp, _ := template.New("").Parse(string(lobbyHTML))

	if err := tmp.Execute(w, "Conference - Lobby"); err != nil {
		log.Fatal(err)
	}

}

func createConferenceHandler(w http.ResponseWriter, r *http.Request) {

	roomUUID := websockets.AddRoomUUID()

	http.Redirect(w, r, "/room/"+roomUUID, 302)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	roomUUID, ok := vars["uuid"]
	if !ok {
		fmt.Println("Идентификатор комнаты отсутствует")
	}

	indexHTML, err := os.ReadFile(path + "/templates/index.html")
	if err != nil {
		panic(err)
	}

	indexTemplate = template.Must(template.New("").Parse(string(indexHTML)))

	if err := indexTemplate.Execute(w, websocketType+host+"/websocket/"+roomUUID+"/join"); err != nil {
		log.Fatal(err)
	}
}
