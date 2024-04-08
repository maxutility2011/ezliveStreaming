package main

import (
	"fmt"
	"errors"
	"os"
	"os/exec"
	"time"
	"net/http"
	"bytes"
	"flag"
	"strings"
	"log"
	"strconv"
	"io/ioutil"
	"encoding/json"
	"container/list"
	"ezliveStreaming/models"
	"ezliveStreaming/job"
)

type WorkerAppConfig struct {
	SchedulerUrl string
	WorkerAppIp string
	WorkerAppPort string
}

type RunningJob struct {
	Job job.LiveJob
	Command *exec.Cmd
}

var liveJobsEndpoint = "jobs"

func createJob(j job.LiveJob) (error, string) {
	j.Time_received_by_worker = time.Now()

	e := createUpdateJob(j)
	if e != nil {
		fmt.Println("Error: Failed to create/update job ID: ", j.Id)
		return e, ""
	}

	j2, ok := getJobById(j.Id) 
	if !ok {
		fmt.Println("Error: Failed to find job ID: ", j.Id)
		return e, ""
	} 

	fmt.Printf("New job created: %+v\n", j2)
	return nil, j.Id
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

	if strings.Contains(r.URL.Path, liveJobsEndpoint) {
		if !(r.Method == "POST" || r.Method == "GET") {
            err := "Method = " + r.Method + " is not allowed to " + r.URL.Path
            fmt.Println(err)
            http.Error(w, "405 method not allowed\n  Error: " + err, http.StatusMethodNotAllowed)
            return
        }

		if r.Method == "POST" && UrlLastPart != liveJobsEndpoint {
			res := "POST to " + r.URL.Path + "is not allowed"
			fmt.Println(res)
			http.Error(w, "400 bad request\n  Error: " + res, http.StatusBadRequest)
		} else if r.Method == "POST" && UrlLastPart == liveJobsEndpoint {
			if r.Body == nil {
            	res := "Error New live job without job specification"
            	fmt.Println("Error New live job without job specifications")
            	http.Error(w, "400 bad request\n  Error: " + res, http.StatusBadRequest)
            	return
        	}

			var job job.LiveJob
			e := json.NewDecoder(r.Body).Decode(&job)
			if e != nil {
            	res := "Failed to decode job request"
            	fmt.Println("Error happened in JSON marshal. Err: %s", e)
            	http.Error(w, "400 bad request\n  Error: " + res, http.StatusBadRequest)
            	return
        	}

			e1, jid := createJob(job)
			if e1 != nil {
				http.Error(w, "500 internal server error\n  Error: ", http.StatusInternalServerError)
				return
			}

			e2 := createIngestUrl(job)
			if e2 != nil {
				fmt.Println("Failed to allocate ingest url. Error: ", e2)
				http.Error(w, "500 Internal server error\n  Error: ", http.StatusInternalServerError)
				return
			}

			j, ok := getJobById(jid)
			if ok {
				fmt.Println("Ingest URL of job id = ", jid, ": ", j.RtmpIngestUrl)
				e3 := launchJob(j)
				if e3 != nil {
					fmt.Println("Failed to launch job id =", jid, " Error: ", e3)
					http.Error(w, "500 Internal serve error\n  Error: ", http.StatusInternalServerError)
					return
				}

				FileContentType := "application/json"
				w.Header().Set("Content-Type", FileContentType)
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(j)
			} else {
				fmt.Println("Failed to get job id = ", jid, " (worker_app.main_server_handler)")
				http.Error(w, "500 internal server error\n  Error: ", http.StatusInternalServerError)
			}
		} else if r.Method == "GET" {
			// Get all jobs: /jobs/
			if UrlLastPart == liveJobsEndpoint {
				FileContentType := "application/json"
        		w.Header().Set("Content-Type", FileContentType)
        		w.WriteHeader(http.StatusOK)
        		json.NewEncoder(w).Encode(jobs)
			} else { // Get one job: /jobs/[job_id]
				job, ok := jobs[UrlLastPart]
				if ok {
					FileContentType := "application/json"
        			w.Header().Set("Content-Type", FileContentType)
        			w.WriteHeader(http.StatusOK)
        			json.NewEncoder(w).Encode(job)
				} else {
					fmt.Println("Non-existent job id: ", UrlLastPart)
                    http.Error(w, "Non-existent job id: " + UrlLastPart, http.StatusNotFound)
				}
			}
		}
	}
}

func readConfig() {
	configFile, err := os.Open(worker_app_config_file_path)
	if err != nil {
		fmt.Println(err)
	}

	defer configFile.Close() 
	config_bytes, _ := ioutil.ReadAll(configFile)
	json.Unmarshal(config_bytes, &worker_app_config)
}

var rtmp_port_base = 1935 // TODO: Make this onfigurable
var max_rtmp_ports = 15 // TODO: Make this configurable
var scheduler_heartbeat_interval = "1s" 
var job_status_check_interval = "2s"
var worker_app_config_file_path = "worker_app_config.json"
var Log *log.Logger
var job_scheduler_url string
// TODO: Need to move "jobs" to Redis or find a way to recover jobs when worker_app crashes
// TODO: Need to constantly monitor job health. Need to re-assign a job to a new worker
//       when the existing worker crashes.
var jobs = make(map[string]job.LiveJob) // Live jobs assigned to this worker
var running_jobs *list.List // Live jobs actively running on this worker
var myWorkerId string
var last_confirmed_heartbeat_time time.Time
var available_rtmp_ports *list.List
var worker_app_config WorkerAppConfig

func getComputeCapacity() string {
	return "5000"
}

func getBandwidthCapacity() string {
	return "100m"
}

func allocateRtmpIngestPort() int {
	var port int
	e := available_rtmp_ports.Front()
	if e != nil {
		port = int(e.Value.(int))
		available_rtmp_ports.Remove(e)
	} else {
		port = -1
	}

	return port
}

// Need to release RTMP port when a job is done
func releaseRtmpPort(port int) {
	available_rtmp_ports.PushBack(port)
}

func createIngestUrl(job job.LiveJob) error {
	rtmp_ingest_port := allocateRtmpIngestPort() // TODO: need to support RTMPS
	// srt_ingest_port := allocateSrtIngestPort() // TODO
	// srt_ingest_port := allocateRtpIngestPort() // TODO
	var err error
	err = nil
	if rtmp_ingest_port < 0 {
		err = errors.New("NotEnoughRtmpIngestPort")
	} else {
		job.RtmpIngestUrl = "rtmp://" + worker_app_config.WorkerAppIp + ":" + strconv.Itoa(rtmp_ingest_port) + "/live/" + job.StreamKey
		// job.SrtIngestUrl = ...
		// job.RtpIngestUrl = ...
	}

	createUpdateJob(job)
	return err
}

func launchJob(j job.LiveJob) error {
	j.Spec.Input.Url = j.RtmpIngestUrl
	b, err := json.Marshal(j.Spec)
	if err != nil {
		fmt.Println("Failed to marshal job output (launchJob). Error: ", err)
		return err
	}
	
	var transcoderArgs []string

	jobIdArg := "-job_id="
	jobIdArg += j.Id
	transcoderArgs = append(transcoderArgs, jobIdArg)

	paramArg := "-param="
	paramArg += string(b[:])
	transcoderArgs = append(transcoderArgs, paramArg)

	fmt.Println("Worker arguments: ", strings.Join(transcoderArgs, " "))
	ffmpegCmd := exec.Command("worker_transcoder", transcoderArgs...)

	var rj RunningJob
	rj.Job = j
	rj.Command = ffmpegCmd
	running_jobs.PushBack(rj)

	var err2 error
	var out []byte
	go func() {
		out, err2 = ffmpegCmd.CombinedOutput()
		if err2 != nil {
        	fmt.Println("Errors running worker transcoder: %v ", string(out))
		}
	}()

	fmt.Println("Transcoder launch log: ", string(out))
	//out, err2 := exec.Command("worker_transcoder", transcoderArgs...).CombinedOutput()
	return err2
}

func checkJobStatus() error {
	fmt.Println("Checking job status... # of running jobs = ", running_jobs.Len())
	for e := running_jobs.Front(); e != nil; e = e.Next() {
		j := RunningJob(e.Value.(RunningJob))
		fmt.Println("Checking status of job id = ", j.Job.Id)
		out, _ := j.Command.CombinedOutput()
		fmt.Println("Transcoder log: ", string(out))
	}

	return nil
}

func sendHeartbeat() error {
	var hb models.WorkerHeartbeat
	hb.Worker_id = myWorkerId
	hb.LastHeartbeatTime = time.Now()
	b, _ := json.Marshal(hb)

	Log.Println("Sending heartbeat at time =", hb.LastHeartbeatTime)
	worker_heartbeat_url := job_scheduler_url + "/" + "heartbeat"
	req, err := http.NewRequest(http.MethodPost, worker_heartbeat_url, bytes.NewReader(b))
    if err != nil {
        fmt.Println("Error: Failed to POST to: ", worker_heartbeat_url)
		// TODO: Need to retry registering new worker instead of giving up
        return errors.New("HeartbeatFailure_fail_to_post")
    }
	
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        fmt.Println("Failed to POST heartbeat: ", worker_heartbeat_url)
		return err
    }
	
    defer resp.Body.Close()
    bodyBytes, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        fmt.Println("Error: Failed to read response body")
        return errors.New("HeartbeatFailure_fail_to_read_scheduler_response")
    }

	// TODO: Need to handle error response (other than http code 200)
	var hb_resp models.WorkerHeartbeat
	json.Unmarshal(bodyBytes, &hb_resp)
	 
	/*if hb_resp.Id == "" {
		fmt.Println("Failed heartbeat. Worker is NOT registered.")
		return errors.New("HeartbeatFailure_unknown_worker")
	}*/

	last_confirmed_heartbeat_time = hb.LastHeartbeatTime
	return nil
}

func registerWorker(conf WorkerAppConfig) error {
	register_new_worker_url := job_scheduler_url + "/" + "workers"
	var new_worker_request models.WorkerInfo
	new_worker_request.ServerIp = conf.WorkerAppIp
	new_worker_request.ServerPort = conf.WorkerAppPort
	new_worker_request.ComputeCapacity = getComputeCapacity()
	new_worker_request.BandwidthCapacity = getBandwidthCapacity()
	new_worker_request.HeartbeatInterval = scheduler_heartbeat_interval
	
	b, _ := json.Marshal(new_worker_request)

	fmt.Println("Registering new worker at: ", register_new_worker_url) 
	req, err := http.NewRequest(http.MethodPost, register_new_worker_url, bytes.NewReader(b))
    if err != nil {
        fmt.Println("Error: Failed to POST to: ", register_new_worker_url)
        return errors.New("http_post_request_creation_failure")
    }
	
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        fmt.Println("Error: Failed to POST to: ", register_new_worker_url)
        return errors.New("http_post_request_send_failure")
    }
	
    defer resp.Body.Close()
    bodyBytes, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        fmt.Println("Error: Failed to read response body")
        return errors.New("http_post_response_parsing_failure")
    }

	var wkr models.LiveWorker
	json.Unmarshal(bodyBytes, &wkr)
	myWorkerId = wkr.Id
	fmt.Println("Worker registered successfully with worker id =", myWorkerId)
	return nil
}

func main() {
	var logfile, err1 = os.Create("/tmp/worker_app.log")
    if err1 != nil {
        panic(err1)
    }

    configPtr := flag.String("config", "", "config file path")
    flag.Parse()

	if *configPtr != "" {
		worker_app_config_file_path = *configPtr
	}

    Log = log.New(logfile, "", log.LstdFlags)
	readConfig()
	job_scheduler_url = worker_app_config.SchedulerUrl

	// Worker app also acts as a client to the job scheduler when registering itself (worker) and report states/stats
	err1 = registerWorker(worker_app_config)
	if err1 != nil {
		fmt.Println("Failed to register worker. Try again later.")
	}

	available_rtmp_ports = list.New()
	for p := rtmp_port_base; p < rtmp_port_base + max_rtmp_ports; p++ {
		available_rtmp_ports.PushBack(p)
	}

	running_jobs = list.New()

	// Periodic heartbeat
	d, _ := time.ParseDuration(scheduler_heartbeat_interval)
	ticker := time.NewTicker(d)
	quit := make(chan struct{})
	go func(ticker *time.Ticker) {
		for {
		   select {
			case <-ticker.C: {
				// Worker ID is assigned by scheduler only when worker registration succeeded.
				if myWorkerId == "" {
					err1 = registerWorker(worker_app_config)
					if err1 != nil {
						fmt.Println("Failed to register worker. Try again later.")
					}
				} else {
					sendHeartbeat() 
				}
			}
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}(ticker)

	// Periodic status check of running jobs
	d2, _ := time.ParseDuration(job_status_check_interval)
	ticker1 := time.NewTicker(d2)
	quit1 := make(chan struct{})
	go func(ticker1 *time.Ticker) {
		for {
		   select {
			case <-ticker1.C: {
				err1 = checkJobStatus()
				if err1 != nil {
					fmt.Println("Failed to check job status. Try again later.")
				}
			}
			case <-quit1:
				ticker1.Stop()
				return
			}
		}
	}(ticker1)

	// Worker app provides web API for handling new job requests received from the job scheduler
	worker_app_addr := worker_app_config.WorkerAppIp + ":" + worker_app_config.WorkerAppPort
	http.HandleFunc("/", main_server_handler)
    fmt.Println("API server listening on: ", worker_app_addr)
    http.ListenAndServe(worker_app_addr, nil)
}