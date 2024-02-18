package websockets

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	listLock        sync.RWMutex
	conferences     = make(map[string]int)
	peerConnections = make(map[string][]peerConnectionState)
	trackLocals     = make(map[string]map[string]*webrtc.TrackLocalStaticRTP)
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

type peerConnectionState struct {
	peerConnection *webrtc.PeerConnection
	websocket      *threadSafeWriter
}

// Helper to make Gorilla Websockets threadsafe
type threadSafeWriter struct {
	*websocket.Conn
	sync.Mutex
}

func (t *threadSafeWriter) WriteJSON(v interface{}) error {
	t.Lock()
	defer t.Unlock()

	return t.Conn.WriteJSON(v)
}

// dispatchKeyFrame sends a keyframe to all PeerConnections, used everytime a new user joins the call
func dispatchKeyFrame(roomUUID string) {
	listLock.Lock()
	defer listLock.Unlock()

	for i := range peerConnections[roomUUID] {
		for _, receiver := range peerConnections[roomUUID][i].peerConnection.GetReceivers() {
			if receiver.Track() == nil {
				continue
			}

			_ = peerConnections[roomUUID][i].peerConnection.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{
					MediaSSRC: uint32(receiver.Track().SSRC()),
				},
			})
		}
	}
}

func AddRoomUUID() string {
	roomUUID := uuid.New()

	conferences[roomUUID.String()] = 1

	return roomUUID.String()
}

func Handler(w http.ResponseWriter, r *http.Request) {

	vars := mux.Vars(r)
	roomUUID, ok := vars["uuid"]
	if !ok {
		fmt.Println("Идентификатор комнаты отсутствует")
	}

	// Upgrade HTTP request to Websocket
	unsafeConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	go func() {
		for range time.NewTicker(time.Second * 3).C {
			dispatchKeyFrame(roomUUID)
		}
	}()

	// When this frame returns close the Websocket
	defer func(c *threadSafeWriter) {
		err := c.Close()
		if err != nil {
			log.Println(err)
		}
	}(c) //nolint

	// Create new PeerConnection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Print(err)
		return
	}

	// When this frame returns close the PeerConnection
	defer func(peerConnection *webrtc.PeerConnection) {
		err := peerConnection.Close()
		if err != nil {
			log.Println(err)
		}
	}(peerConnection) //nolint

	// Accept one audio and one video track incoming
	for _, typ := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := peerConnection.AddTransceiverFromKind(typ, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		}); err != nil {
			log.Print(err)
			return
		}
	}

	// Add our new PeerConnection to global list
	listLock.Lock()
	peerConnections[roomUUID] = append(peerConnections[roomUUID], peerConnectionState{peerConnection, c})
	listLock.Unlock()

	// Trickle ICE. Emit server candidate to client
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		if i == nil {
			return
		}

		candidateString, err := json.Marshal(i.ToJSON())
		if err != nil {
			log.Println(err)
			return
		}

		if writeErr := c.WriteJSON(&websocketMessage{
			Event: "candidate",
			Data:  string(candidateString),
		}); writeErr != nil {
			log.Println(writeErr)
		}
	})

	// If PeerConnection is closed remove it from global list
	peerConnection.OnConnectionStateChange(func(p webrtc.PeerConnectionState) {
		switch p {
		case webrtc.PeerConnectionStateFailed:
			if err := peerConnection.Close(); err != nil {
				log.Print(err)
			}
		case webrtc.PeerConnectionStateClosed:
			signalPeerConnections(roomUUID)
		default:
		}
	})

	peerConnection.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		// Create a track to fan out our incoming video to all peers
		trackLocal := addTrack(t, roomUUID)
		defer removeTrack(trackLocal, roomUUID)

		buf := make([]byte, 1500)
		for {
			i, _, err := t.Read(buf)
			if err != nil {
				return
			}

			if _, err = trackLocal.Write(buf[:i]); err != nil {
				return
			}
		}
	})

	// Signal for the new PeerConnection
	signalPeerConnections(roomUUID)

	message := &websocketMessage{}
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			log.Println(err)
			return
		} else if err := json.Unmarshal(raw, &message); err != nil {
			log.Println(err)
			return
		}

		switch message.Event {
		case "candidate":
			candidate := webrtc.ICECandidateInit{}
			if err := json.Unmarshal([]byte(message.Data), &candidate); err != nil {
				log.Println(err)
				return
			}

			if err := peerConnection.AddICECandidate(candidate); err != nil {
				log.Println(err)
				return
			}
		case "answer":
			answer := webrtc.SessionDescription{}
			if err := json.Unmarshal([]byte(message.Data), &answer); err != nil {
				log.Println(err)
				return
			}

			if err := peerConnection.SetRemoteDescription(answer); err != nil {
				log.Println(err)
				return
			}
		}
	}
}

// signalPeerConnections updates each PeerConnection so that it is getting all the expected media tracks
func signalPeerConnections(roomUUID string) {
	listLock.Lock()
	defer func() {
		listLock.Unlock()
		dispatchKeyFrame(roomUUID)
	}()

	attemptSync := func() (tryAgain bool) {
		for i := range peerConnections[roomUUID] {
			if peerConnections[roomUUID][i].peerConnection.ConnectionState() == webrtc.PeerConnectionStateClosed {
				peerConnections[roomUUID] = append(peerConnections[roomUUID][:i], peerConnections[roomUUID][i+1:]...)
				return true // We modified the slice, start from the beginning
			}

			// map of sender we already are seanding, so we don't double send
			existingSenders := map[string]bool{}

			for _, sender := range peerConnections[roomUUID][i].peerConnection.GetSenders() {
				if sender.Track() == nil {
					continue
				}

				existingSenders[sender.Track().ID()] = true

				// If we have a RTPSender that doesn't map to a existing track remove and signal
				if _, ok := trackLocals[roomUUID][sender.Track().ID()]; !ok {
					if err := peerConnections[roomUUID][i].peerConnection.RemoveTrack(sender); err != nil {
						return true
					}
				}
			}

			// Don't receive videos we are sending, make sure we don't have loopback
			for _, receiver := range peerConnections[roomUUID][i].peerConnection.GetReceivers() {
				if receiver.Track() == nil {
					continue
				}

				existingSenders[receiver.Track().ID()] = true
			}

			// Add all track we aren't sending yet to the PeerConnection
			for trackID := range trackLocals[roomUUID] {
				if _, ok := existingSenders[trackID]; !ok {
					if _, err := peerConnections[roomUUID][i].peerConnection.AddTrack(trackLocals[roomUUID][trackID]); err != nil {
						return true
					}
				}
			}

			offer, err := peerConnections[roomUUID][i].peerConnection.CreateOffer(nil)
			if err != nil {
				return true
			}

			if err = peerConnections[roomUUID][i].peerConnection.SetLocalDescription(offer); err != nil {
				return true
			}

			offerString, err := json.Marshal(offer)
			if err != nil {
				return true
			}

			if err = peerConnections[roomUUID][i].websocket.WriteJSON(&websocketMessage{
				Event: "offer",
				Data:  string(offerString),
			}); err != nil {
				return true
			}
		}

		return
	}

	for syncAttempt := 0; ; syncAttempt++ {
		if syncAttempt == 25 {
			// Release the lock and attempt a sync in 3 seconds. We might be blocking a RemoveTrack or AddTrack
			go func() {
				time.Sleep(time.Second * 3)
				signalPeerConnections(roomUUID)
			}()
			return
		}

		if !attemptSync() {

			break
		}
	}
}

// Add to list of tracks and fire renegotation for all PeerConnections
func addTrack(t *webrtc.TrackRemote, roomUUID string) *webrtc.TrackLocalStaticRTP {

	listLock.Lock()
	defer func() {
		listLock.Unlock()
		signalPeerConnections(roomUUID)
	}()

	// Create a new TrackLocal with the same codec as our incoming
	trackLocal, err := webrtc.NewTrackLocalStaticRTP(t.Codec().RTPCodecCapability, t.ID(), t.StreamID())
	if err != nil {
		panic(err)
	}

	if _, exist := trackLocals[roomUUID]; !exist {
		trackLocals[roomUUID] = make(map[string]*webrtc.TrackLocalStaticRTP)
	}

	trackLocals[roomUUID][t.ID()] = trackLocal
	return trackLocal
}

// Remove from list of tracks and fire renegotation for all PeerConnections
func removeTrack(t *webrtc.TrackLocalStaticRTP, roomUUID string) {
	listLock.Lock()

	defer func() {
		listLock.Unlock()
		signalPeerConnections(roomUUID)
	}()

	delete(trackLocals[roomUUID], t.ID())
}
