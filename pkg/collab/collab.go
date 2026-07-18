package collab

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/replica"

	"github.com/gorilla/websocket"
	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/encoding"
	ygsync "github.com/reearth/ygo/sync"
)

// Hocuspocus message tags
const (
	msgSync               = 0
	msgAwareness          = 1
	msgAuth               = 2
	msgQueryAwareness     = 3
	msgSyncReply          = 4
	msgStateless          = 5
	msgBroadcastStateless = 6
	msgClose              = 7
	msgSyncStatus         = 8
	msgPing               = 9
	msgPong               = 10
)

type ScopedDocumentName struct {
	UserId       string
	ManuscriptId string
	ChapterId    string
}

func parseDocumentName(docName string) (*ScopedDocumentName, error) {
	if len(docName) > 260 {
		return nil, fmt.Errorf("document name too long")
	}
	slash := strings.Index(docName, "/")
	if slash <= 0 {
		return nil, fmt.Errorf("invalid document scope")
	}
	colon := strings.Index(docName[slash+1:], ":")
	if colon <= 0 {
		return nil, fmt.Errorf("invalid document scope")
	}
	actualColon := slash + 1 + colon
	userId, err := url.QueryUnescape(docName[:slash])
	if err != nil {
		return nil, fmt.Errorf("failed to unescape userId: %w", err)
	}
	manuscriptId := docName[slash+1 : actualColon]
	chapterId := docName[actualColon+1:]

	return &ScopedDocumentName{
		UserId:       userId,
		ManuscriptId: manuscriptId,
		ChapterId:    chapterId,
	}, nil
}

type Hub struct {
	mu         sync.Mutex
	rooms      map[string]*Room
	db         *sql.DB
	cfg        *config.Config
	upgrader   websocket.Upgrader
	shutdownCh chan struct{}
}

func NewHub(database *sql.DB, cfg *config.Config) *Hub {
	h := &Hub{
		rooms: make(map[string]*Room),
		db:    database,
		cfg:   cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Gate CORS at Authorize step
			},
		},
		shutdownCh: make(chan struct{}),
	}
	go h.backgroundSaver()
	return h
}

func (h *Hub) Close() {
	close(h.shutdownCh)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, room := range h.rooms {
		room.Close()
	}
}

type Room struct {
	mu           sync.Mutex
	name         string
	parsed       *ScopedDocumentName
	doc          *crdt.Doc
	peers        map[*Peer]bool
	dirty        bool
	lastModified int64
	db           *sql.DB
}

type Peer struct {
	room   *Room
	conn   *websocket.Conn
	sendCh chan []byte
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse room name from URL Path value or segment
	roomName := r.PathValue("room")
	if roomName == "" {
		roomName = strings.TrimPrefix(r.URL.Path, "/collab/")
		roomName = strings.TrimPrefix(roomName, "/")
	}

	if roomName == "" {
		http.Error(w, "missing room name", http.StatusBadRequest)
		return
	}

	parsed, err := parseDocumentName(roomName)
	if err != nil {
		http.Error(w, "invalid room name scope", http.StatusBadRequest)
		return
	}

	// 1. Authenticate user
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.URL.Query().Get("auth_token")
	}
	if token == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			token = authHeader[7:]
		}
	}

	userId, authorized := h.resolveUser(token)
	if !authorized || userId != parsed.UserId {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Verify chapter exists and belongs to the user
	var exists int
	err = h.db.QueryRow(`
		SELECT 1 FROM chapters
		WHERE user_id = ? AND manuscript_id = ? AND id = ? AND deleted_at IS NULL
	`, parsed.UserId, parsed.ManuscriptId, parsed.ChapterId).Scan(&exists)
	if err != nil {
		http.Error(w, "chapter not found", http.StatusNotFound)
		return
	}

	// 3. Upgrade to websocket connection
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	h.mu.Lock()
	room, ok := h.rooms[roomName]
	if !ok {
		// Initialize the room and load/seed its document
		room = &Room{
			name:   roomName,
			parsed: parsed,
			doc:    crdt.New(),
			peers:  make(map[*Peer]bool),
			db:     h.db,
		}
		if err := h.loadDocState(room); err != nil {
			log.Printf("[collab] failed to load doc state for %q: %v", roomName, err)
		}
		h.rooms[roomName] = room
	}
	h.mu.Unlock()

	peer := &Peer{
		room:   room,
		conn:   conn,
		sendCh: make(chan []byte, 256),
	}

	room.mu.Lock()
	room.peers[peer] = true
	room.mu.Unlock()

	// Send initial Sync Step 1 to peer
	room.mu.Lock()
	step1 := ygsync.EncodeSyncStep1(room.doc)
	room.mu.Unlock()
	peer.send(encodeHocuspocusFrame(roomName, msgSync, step1))

	// Start reader & writer loops
	go peer.writer()
	peer.reader()
}

func (h *Hub) resolveUser(token string) (string, bool) {
	switch h.cfg.Auth.Mode {
	case config.AuthModeNone:
		return db.LocalUserID, true
	case config.AuthModeToken:
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.Auth.Token)) == 1 {
			return db.LocalUserID, true
		}
		return "", false
	case config.AuthModeOIDC:
		if token == "" {
			return "", false
		}
		var userID string
		var expiresAt int64
		err := h.db.QueryRow("SELECT user_id, expires_at FROM sessions WHERE token = ?", token).Scan(&userID, &expiresAt)
		now := time.Now().UnixNano() / int64(time.Millisecond)
		if err != nil || expiresAt < now {
			return "", false
		}
		return userID, true
	case config.AuthModeForward:
		return db.LocalUserID, true
	default:
		return "", false
	}
}

func (h *Hub) loadDocState(room *Room) error {
	var data []byte
	err := h.db.QueryRow("SELECT data FROM ydocs WHERE name = ?", room.name).Scan(&data)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if len(data) > 0 {
		return room.doc.ApplyUpdate(data)
	}

	// Seed from chapter content HTML
	var content sql.NullString
	err = h.db.QueryRow(`
		SELECT content FROM chapters
		WHERE user_id = ? AND manuscript_id = ? AND id = ? AND deleted_at IS NULL
	`, room.parsed.UserId, room.parsed.ManuscriptId, room.parsed.ChapterId).Scan(&content)
	if err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}

	if content.Valid && content.String != "" {
		seededDoc, err := HTMLToYDoc(content.String)
		if err != nil {
			return err
		}
		update := crdt.EncodeStateAsUpdateV1(seededDoc, nil)
		_, _ = h.db.Exec(`
			INSERT INTO ydocs (name, data, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at
		`, room.name, update, time.Now().UnixMilli())
		return room.doc.ApplyUpdate(update)
	}

	return nil
}

func (p *Peer) reader() {
	defer func() {
		p.room.removePeer(p)
		p.conn.Close()
	}()

	for {
		_, data, err := p.conn.ReadMessage()
		if err != nil {
			break
		}

		docName, outerType, payload, err := decodeHocuspocusFrame(data)
		if err != nil {
			continue
		}

		if docName != p.room.name {
			continue // Scope mismatch
		}

		switch outerType {
		case msgSync, msgSyncReply:
			p.room.mu.Lock()
			reply, err := ygsync.ApplySyncMessage(p.room.doc, payload, p)
			if err == nil {
				p.room.dirty = true
				p.room.lastModified = time.Now().UnixMilli()
			}
			p.room.mu.Unlock()

			if err != nil {
				continue
			}

			if reply != nil && outerType == msgSync {
				p.send(encodeHocuspocusFrame(docName, msgSync, reply))
			} else {
				// Broadcast update payload
				p.room.broadcast(p, encodeHocuspocusFrame(docName, msgSync, payload))
			}

		case msgAwareness:
			// Simply relay awareness updates to all other peers
			p.room.broadcast(p, data)

		case msgPing:
			p.send(encodeHocuspocusFrame(docName, msgPong, nil))

		case msgClose:
			return
		}
	}
}

func (p *Peer) writer() {
	for msg := range p.sendCh {
		_ = p.conn.WriteMessage(websocket.BinaryMessage, msg)
	}
}

func (p *Peer) send(msg []byte) {
	select {
	case p.sendCh <- msg:
	default:
		// Queue full, disconnect peer
		p.conn.Close()
	}
}

func (room *Room) removePeer(p *Peer) {
	room.mu.Lock()
	delete(room.peers, p)
	close(p.sendCh)
	empty := len(room.peers) == 0
	room.mu.Unlock()

	if empty {
		// Room is now empty, we can clean it up
		// Force save final state immediately
		room.saveState(room.db)
	}
}

func (room *Room) broadcast(sender *Peer, msg []byte) {
	room.mu.Lock()
	defer room.mu.Unlock()
	for p := range room.peers {
		if p != sender {
			p.send(msg)
		}
	}
}

func (room *Room) Close() {
	room.mu.Lock()
	defer room.mu.Unlock()
	for p := range room.peers {
		p.conn.Close()
	}
}

func (room *Room) saveState(dbConn *sql.DB) {
	room.mu.Lock()
	if !room.dirty {
		room.mu.Unlock()
		return
	}
	state := crdt.EncodeStateAsUpdateV1(room.doc, nil)
	room.dirty = false
	parsed := room.parsed
	lastModified := room.lastModified
	room.mu.Unlock()

	// Perform database saves inside a transaction
	tx, err := dbConn.Begin()
	if err != nil {
		log.Printf("[collab] failed to start save transaction for %s: %v", room.name, err)
		return
	}
	defer tx.Rollback()

	// 1. Fetch current chapter metadata
	var title sql.NullString
	var position int
	var cRevision int
	var currentContent sql.NullString
	err = tx.QueryRow(`
		SELECT title, position, revision, content FROM chapters
		WHERE user_id = ? AND manuscript_id = ? AND id = ? AND deleted_at IS NULL
	`, parsed.UserId, parsed.ManuscriptId, parsed.ChapterId).Scan(&title, &position, &cRevision, &currentContent)
	if err == sql.ErrNoRows {
		return // Chapter deleted
	} else if err != nil {
		log.Printf("[collab] failed to query chapter metadata for %s: %v", room.name, err)
		return
	}

	// 2. Save YDoc to ydocs table
	_, err = tx.Exec(`
		INSERT INTO ydocs (name, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at
	`, room.name, state, lastModified)
	if err != nil {
		log.Printf("[collab] failed to upsert ydoc state for %s: %v", room.name, err)
		return
	}

	// 3. Render HTML snapshot
	doc := crdt.New()
	if err := doc.ApplyUpdate(state); err != nil {
		log.Printf("[collab] failed to parse ydoc for snapshot rendering of %s: %v", room.name, err)
		return
	}
	htmlStr, err := YDocToHTML(doc)
	if err != nil {
		log.Printf("[collab] failed to render HTML snapshot for %s: %v", room.name, err)
		return
	}

	if currentContent.Valid && currentContent.String == htmlStr {
		tx.Commit()
		return // No content changes
	}

	// 4. Save pre-collab backup if none exists
	var hasBackup int
	err = tx.QueryRow(`
		SELECT 1 FROM chapter_pre_collab
		WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
	`, parsed.UserId, parsed.ManuscriptId, parsed.ChapterId).Scan(&hasBackup)
	if err == sql.ErrNoRows {
		prevContent := ""
		if currentContent.Valid {
			prevContent = currentContent.String
		}
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO chapter_pre_collab (user_id, manuscript_id, chapter_id, content, backed_up_at)
			VALUES (?, ?, ?, ?, ?)
		`, parsed.UserId, parsed.ManuscriptId, parsed.ChapterId, prevContent, lastModified)
		if err != nil {
			log.Printf("[collab] failed to insert pre-collab backup for %s: %v", room.name, err)
			return
		}
	} else if err != nil {
		log.Printf("[collab] failed to query pre-collab backup for %s: %v", room.name, err)
		return
	}

	// 5. Update chapter content in DB
	newRevision := cRevision + 1
	_, err = tx.Exec(`
		UPDATE chapters
		   SET content = ?, last_modified = ?, revision = ?
		 WHERE user_id = ? AND manuscript_id = ? AND id = ?
	`, htmlStr, lastModified, newRevision, parsed.UserId, parsed.ManuscriptId, parsed.ChapterId)
	if err != nil {
		log.Printf("[collab] failed to update chapter HTML for %s: %v", room.name, err)
		return
	}

	// 6. Record change log
	_, err = db.RecordChange(tx, parsed.UserId, "chapter", &parsed.ManuscriptId, parsed.ChapterId, "upsert", newRevision, lastModified)
	if err != nil {
		log.Printf("[collab] failed to record change log for %s: %v", room.name, err)
		return
	}

	// 7. Touch manuscript
	_, err = db.TouchManuscriptForChapterChange(tx, parsed.UserId, parsed.ManuscriptId, lastModified)
	if err != nil {
		log.Printf("[collab] failed to touch manuscript for %s: %v", room.name, err)
		return
	}

	// 8. Enqueue replica outbox
	titleVal := ""
	if title.Valid {
		titleVal = title.String
	}
	replData := replica.SerializeChapter(parsed.UserId, parsed.ManuscriptId, parsed.ChapterId, titleVal, position, lastModified, newRevision, htmlStr)
	err = replica.EnqueueReplicaPut(tx, fmt.Sprintf("manuscripts/%s/%s/chapters/%s.html", parsed.UserId, parsed.ManuscriptId, parsed.ChapterId), replData, "text/html; charset=utf-8")
	if err != nil {
		log.Printf("[collab] failed to enqueue chapter replica for %s: %v", room.name, err)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[collab] failed to commit collab save transaction for %s: %v", room.name, err)
	}
}

func (h *Hub) backgroundSaver() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.saveAllDirty()
		case <-h.shutdownCh:
			h.saveAllDirty()
			return
		}
	}
}

func (h *Hub) saveAllDirty() {
	h.mu.Lock()
	var roomsToSave []*Room
	for _, room := range h.rooms {
		room.mu.Lock()
		dirty := room.dirty
		room.mu.Unlock()
		if dirty {
			roomsToSave = append(roomsToSave, room)
		}
	}
	h.mu.Unlock()

	for _, room := range roomsToSave {
		room.saveState(h.db)
	}
}

func encodeHocuspocusFrame(docName string, outerType uint64, payload []byte) []byte {
	enc := encoding.NewEncoder()
	enc.WriteVarString(docName)
	enc.WriteVarUint(outerType)
	if len(payload) > 0 {
		enc.WriteRaw(payload)
	}
	return enc.Bytes()
}

func decodeHocuspocusFrame(data []byte) (string, uint64, []byte, error) {
	dec := encoding.NewDecoder(data)
	docName, err := dec.ReadVarString()
	if err != nil {
		return "", 0, nil, err
	}
	outerType, err := dec.ReadVarUint()
	if err != nil {
		return "", 0, nil, err
	}
	return docName, outerType, dec.RemainingBytes(), nil
}
