package main

import (
	"fmt"
	"net/http"
	"strings"
	"encoding/json"
	"github.com/google/uuid"
	"os/exec"
	"log"
	//"io/ioutil"
	"ezliveStreaming/job"
)

var createLiveJobEndpoint = "createLiveJob"
/*
type liveVideoOutputSpec struct {
	//video_output_label string `json:"label"`
	Video_codec string `json:"codec"`
	Video_framerate_fps float32 `json:"framerate"`
	Video_width int `json:"width"`
	Video_height int `json:"height"`
	Video_bitrate_kbps float32 `json:"bitrate"`
	Video_gop_size_sec int `json:"gop_size"`
}

type liveAudioOutputSpec struct {
	//audio_output_label string `json:"label"`
	Audio_codec string `json:"codec"`
	Audio_bitrate_kbps float32 `json:"bitrate"`
}

type liveJobOutputSpec struct {
	Output_stream_type string `json:"stream_type"`
	Output_segment_format string `json:"segment_format"`
	Output_segment_duration_sec int `json:"segment_duration"`
	Video_outputs []liveVideoOutputSpec `json:"video_outputs"`
	Audio_outputs []liveAudioOutputSpec `json:"audio_outputs"`
}

type liveJobInputSpec struct {
	Input_url string `json:"url"`
}

type liveJobSpec struct {
    Job_input liveJobInputSpec `json:"input"`
    Job_output liveJobOutputSpec `json:"output"`
}
*/

/*
type LiveVideoOutputSpec struct {
	//video_output_label string `json:"label"`
	Codec string 
	Framerate float32
	Width int
	Height int
	Bitrate float32 
	Gop_size int 
}

type LiveAudioOutputSpec struct {
	//audio_output_label string `json:"label"`
	Codec string 
	Bitrate float32 
}

type LiveJobOutputSpec struct {
	Stream_type string 
	Segment_format string 
	Segment_duration int 
	Video_outputs []LiveVideoOutputSpec 
	Audio_outputs []LiveAudioOutputSpec 
}

type LiveJobInputSpec struct {
	Url string 
}

type LiveJobSpec struct {
    Input LiveJobInputSpec 
    Output LiveJobOutputSpec 
}

type LiveJob struct {
	Id string
	Spec LiveJobSpec
}
*/

var jobs = make(map[string]job.LiveJob)

func createJob(j job.LiveJobSpec) error {
	var lj job.LiveJob
	lj.Id = uuid.New().String()
	lj.Spec = j
	fmt.Println("Generating a random job ID: ", lj.Id)

	e := createUpdateJob(lj)
	if e != nil {
		fmt.Println("Error: Failed to create/update job ID: ", lj.Id)
		return e
	}

	j2, ok := getJobById(lj.Id) 
	if ok {
		fmt.Printf("New job created: %+v\n", j2)
		return nil
	} 

	return nil
}

func createUpdateJob(j job.LiveJob) error {
	jobs[j.Id] = j
	return nil
}

func getJobById(jid string) (job.LiveJob, bool) {
	job, ok := jobs[jid]
	return job, ok
}

func main_server_handler(w http.ResponseWriter, r *http.Request) {
    fmt.Println("----------------------------------------")
    fmt.Println("Received new request:")
    fmt.Println(r.Method, r.URL.Path)

    posLastSingleSlash := strings.LastIndex(r.URL.Path, "/")
    UrlLastPart := r.URL.Path[posLastSingleSlash + 1 :]

    // Remove trailing "/" if any
    if len(UrlLastPart) == 0 {
        path_without_trailing_slash := r.URL.Path[0 : posLastSingleSlash]
        posLastSingleSlash = strings.LastIndex(path_without_trailing_slash, "/")
        UrlLastPart = path_without_trailing_slash[posLastSingleSlash + 1 :]
    } 

	if UrlLastPart == createLiveJobEndpoint {
		if r.Method != "POST" {
            err := "Method = " + r.Method + " is not allowed to " + r.URL.Path
            fmt.Println(err)
            http.Error(w, "405 method not allowed\n  Error: " + err, http.StatusMethodNotAllowed)
            return
        }

		if r.Body == nil {
            res := "Error New live job without job specification"
            fmt.Println("Error New live job without job specifications")
            http.Error(w, "400 bad request\n  Error: " + res, http.StatusBadRequest)
            return
        }

		var job job.LiveJobSpec
		e := json.NewDecoder(r.Body).Decode(&job)
		if e != nil {
            res := "Failed to decode job request"
            fmt.Println("Error happened in JSON marshal. Err: %s", e)
            http.Error(w, "400 bad request\n  Error: " + res, http.StatusBadRequest)
            return
        }

		//fmt.Println("Header: ", r.Header)
		//fmt.Printf("Job: %+v\n", job)
		//fmt.Println(job.input.url);

		e = createJob(job)
		if e != nil {
			http.Error(w, "500 internal server error\n  Error: ", http.StatusInternalServerError)
		}

		b, _ := json.Marshal(job)
		//fmt.Println(string(b[:]))
		paramArg := "-param=" 
		paramArg += string(b[:])
		out, err2 := exec.Command("worker", paramArg).CombinedOutput()
    	if err2 != nil {
        	// error case : status code of command is different from 0
        	log.Fatal("ffmpeg error: %v", err2, string(out))
    	}
		
	}
}

var server_ip = "0.0.0.0"
var server_port = "1080" 
var server_addr = server_ip + ":" + server_port

func main() {
	http.HandleFunc("/", main_server_handler)

    fmt.Println("API server listening on: ", server_addr)
    http.ListenAndServe(server_addr, nil)
}