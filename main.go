package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	_ "github.com/mattn/go-sqlite3"
)

const headers = `HTTP/1.x 200 OK
Cache-Control: no-cache
Transfer-Encoding: chunked
Content-Type: text/plain; charset=iso-8859-1
X-Content-Type-Options: nosniff

`

var cli struct {
	Payload string `arg name:"payload" help:"content to send as a response." type:"string"`
	Port    string `arg name:"port" help:"port to bind to." type:"int" default:8080 optional`
}

func main() {
	kong.Parse(&cli,
		kong.Name("reverse-slowloris"),
		kong.Description("A server that sends a slow HTTP response forever to whoever connects to it."))

	db, err := sql.Open("sqlite3", "db.sqlite3")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	createTable(db)

	var requestNum = getStartingID(db)

	payload, err := ioutil.ReadFile(cli.Payload)
	if err != nil {
		log.Fatalf("Failed to load %s: %s", cli.Payload, err)
	}

	log.Printf("Starting server on port %s", cli.Port)
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", cli.Port))
	if err != nil {
		log.Fatalf("Failed to start server :%s", err)
	}

	// Close the listener when the application closes.
	defer listener.Close()
	for {
		// Listen for an incoming connection.
		conn, err := listener.Accept()
		if err != nil {
			log.Fatalf("Error accepting: ", err.Error())
		}
		// Handle connections in a new goroutine.
		go handleRequest(conn, db, requestNum, payload)
		requestNum++
	}
}

func getProbableRemoteIP(request *http.Request, conn net.Conn) string {
	// Hopefully this is set if there is a proxy like nginx in front
	requester := request.Header.Get("X-Forwarded-For")
	if requester == "" {
		requester = conn.RemoteAddr().String()
	}
	return requester
}

func getParsedRequest(conn net.Conn) (*http.Request, error) {
	buf := make([]byte, 2048)
	_, err := conn.Read(buf)
	if err != nil {
		log.Println("Error reading:", err.Error())
		return nil, errors.New("Error reading headers from socket")
	}
	readRequest, err := http.ReadRequest(bufio.NewReader(strings.NewReader(string(buf))))
	if err != nil {
		log.Println("Error parsing:", err.Error())
		return nil, errors.New("Error parsing headers")
	}
	return readRequest, nil
}

func handleRequest(conn net.Conn, db *sql.DB, requestNum int, payload []byte) {
	defer conn.Close()
	started := time.Now()
	parsedRequest, err := getParsedRequest(conn)
	if err != nil {
		log.Printf("%d | %s", requestNum, err)
		return
	}
	requester := getProbableRemoteIP(parsedRequest, conn)
	conn.Write([]byte(headers))

	path := parsedRequest.URL.RequestURI()

	log.Printf(
		"%d | %s connected asking for %s, starting to stream response\n",
		requestNum,
		requester,
		path,
	)
	keepGoing := true
	for {
		if !keepGoing {
			break
		}
		for _, char := range payload {
			_, err := conn.Write([]byte{uint8(char)})
			// A failure here is expected because it is the only way out of this infinite response
			if err != nil {
				keepGoing = false
				break
			}
			upsertConnectionRow(db, requestNum, requester, path, int64(time.Since(started)/time.Millisecond), started.Unix(), true)
			time.Sleep(75 * time.Millisecond)
		}
		if keepGoing {
			log.Printf("%d | %s Has been streaming for %s\n", requestNum, requester, time.Since(started))
		}
	}
	elapsed := time.Since(started)
	upsertConnectionRow(db, requestNum, requester, path, int64(elapsed/time.Millisecond), started.Unix(), false)
	log.Printf("%d | %s closed their connection after %s\n", requestNum, requester, elapsed)
}

func createTable(db *sql.DB) {
	createStudentTableSQL := `CREATE TABLE IF NOT EXISTS connections (
		"id" integer NOT NULL PRIMARY KEY AUTOINCREMENT,		
		"source_ip" TEXT,
		"path" TEXT,
		"duration" INTEGER,
		"started_at" INTEGER,
		"in_progress" INTEGER
	  );`

	log.Println("Creating table for connections")
	statement, err := db.Prepare(createStudentTableSQL)
	if err != nil {
		log.Fatalln("Invalid statment or something: " + err.Error())
	}
	statement.Exec()
}

func upsertConnectionRow(db *sql.DB, id int, source_ip string, path string, duration int64, started_at int64, in_progress bool) {
	insertSQL := `INSERT INTO connections(id, source_ip, path, duration, started_at, in_progress)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					source_ip=excluded.source_ip,
					path=excluded.path,
					duration=excluded.duration,
					started_at=excluded.started_at,
					in_progress=excluded.in_progress`
	statement, err := db.Prepare(insertSQL)
	if err != nil {
		log.Fatalln(err.Error())
	}
	_, err = statement.Exec(id, source_ip, path, duration, started_at, in_progress)
	if err != nil {
		log.Fatalln(err.Error())
	}
}

func getStartingID(db *sql.DB) int {
	sql := `SELECT MAX(id) AS id FROM connections`
	var id int
	row := db.QueryRow(sql)
	switch err := row.Scan(&id); err {
	case nil:
		return id + 1
	default:
		return 0
	}
}
