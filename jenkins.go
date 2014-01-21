package jenkins

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"time"
)

const DEFAULT_SERVER string = "alfred-jenkins.sv2:8080"

var JENKINS_SERVER string = DEFAULT_SERVER

type JenkinsInfo struct {
	Name               string
	Description        string
	Url                string
	Buildable          bool
	InQueue            bool
	LastBuild          int
	LastBuildUrl       string
	LastStableBuild    int
	LastStableBuildUrl string
}

func (self *JenkinsInfo) Print() {
	log.Println("Job Info For", self.Name)
	log.Println("  description        :", self.Description)
	log.Println("  url                :", self.Url)
	log.Println("  buildable          :", self.Buildable)
	log.Println("  inQueue            :", self.InQueue)
	log.Println("  lastBuild          :", self.LastBuild)
	log.Println("  lastBuildUrl       :", self.LastBuildUrl)
	log.Println("  lastStableBuild    :", self.LastStableBuild)
	log.Println("  lastStableBuildUrl :", self.LastStableBuildUrl)
}

type JenkinsBuildInfo struct {
	Name              string
	Id                int
	Artifacts         map[string]string
	Building          bool
	Duration          float64
	EstimatedDuration float64
	Result            string
	Timestamp         float64
	Url               string
}

func (self *JenkinsBuildInfo) Print() {
	log.Println("Build Info For", self.Name)
	log.Println("  id                :", self.Id)
	log.Println("  artifacts         :", self.Artifacts)
	log.Println("  building          :", self.Building)
	log.Println("  duration          :", strconv.FormatFloat(self.Duration, 'f', -1, 64))
	log.Println("  estimatedDuration :", strconv.FormatFloat(self.EstimatedDuration, 'f', -1, 64))
	log.Println("  result            :", self.Result)
	log.Println("  timestamp         :", strconv.FormatFloat(self.Timestamp, 'f', -1, 64))
	log.Println("  url               :", self.Url)
}

func sanitizeId(name string, id int) (int, error) {
	if id == -1 {
		info, err := GetInfo(name)
		if err != nil {
			return id, err
		}
		if info.LastBuild == 0 {
			return id, errors.New("no build available")
		}
		id = info.LastBuild
	} else if id == -2 {
		info, err := GetInfo(name)
		if err != nil {
			return id, err
		}
		if info.LastStableBuild == 0 {
			return id, errors.New("no stable build available")
		}
		id = info.LastStableBuild
	}
	return id, nil
}

func getRemote(theurl string) (io.ReadCloser, error) {
	//log.Print("Get ", theurl)
	resp, err := http.Get(theurl)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, errors.New("Bad status: " + strconv.Itoa(resp.StatusCode) + " from " + theurl)
	}
	return resp.Body, nil
}

func get(name string, id int) (map[string]interface{}, error) {
	// build URL
	nameAndId := name
	if id > 0 {
		nameAndId = path.Join(name, strconv.Itoa(id))
	}
	theurl := "http://" + path.Join(JENKINS_SERVER, "job", nameAndId, "api", "json")
	resp, err := getRemote(theurl)
	if err != nil {
		return nil, err
	}
	defer resp.Close()
	jsonDecoder := json.NewDecoder(resp)
	retVal := make(map[string]interface{})
	errJson := jsonDecoder.Decode(&retVal)
	if errJson != nil {
		return nil, errJson
	}
	return retVal, nil
}

func post(name string, action string, params string) error {
	theurl := "http://" + path.Join(JENKINS_SERVER, "job", name, "buildWithParameters") + "?token=" + name + "-token"
	form, err := url.ParseQuery(params)
	if err != nil {
		return err
	}
	resp, err := http.PostForm(theurl, form)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func DoBuild(name, params string, wait bool) (*JenkinsBuildInfo, error) {
	log.Print("Building ", name)
	info, err := GetInfo(name)
	if err != nil {
		return nil, err
	}
	newBuild := info.LastBuild + 1
	if info.InQueue {
		log.Print("Job already in queue.")
	} else {
		err := post(name, "buildWithParameters", params)
		if err != nil {
			return nil, err
		}
		log.Print("Build #", newBuild, " scheduled.")
	}
	if !wait {
		return nil, nil
	}
	binfo, err := GetBuildInfo(name, info.LastStableBuild)
	if err != nil {
		return nil, errors.New("Couldn't fetch last stable build info")
	}
	log.Print("Waiting for job to complete. Last stable took ",
		strconv.FormatFloat(binfo.Duration, 'f', -1, 64), " milliseconds.")
	inQueue := false
	building := false
	weird := false
	for {
		binfo, err = GetBuildInfo(name, newBuild)
		if err == nil && !binfo.Building {
			return binfo, nil
		} else if err != nil {
			info, err := GetInfo(name)
			if err != nil {
				return nil, err
			}
			if !info.InQueue || info.LastBuild+1 != newBuild {
				// huh? thats weird. maybe something crazy happened. lets do one more pass
				if weird {
					return nil, errors.New("weird state. could not wait for job to complete.")
					weird = true
				}
			}
			if info.InQueue {
				if !inQueue {
					log.Print("Job is in queue.")
					inQueue = true
				}
			}
		} else if binfo.Building {
			if !building {
				log.Print("Job is building.")
				building = true
			}
		}
		time.Sleep(1000 * time.Millisecond)
	}
	// TODO: wait for build to finish and return the info
	return nil, nil
}

func GetArtifactReader(name string, id int, artifact string) (io.ReadCloser, error) {
	info, err := GetBuildInfo(name, id)
	if err != nil {
		return nil, err
	}
	if info.Result != "SUCCESS" {
		return nil, errors.New("the build you requested failed")
	}
	nameAndId := path.Join(name, strconv.Itoa(id))
	url := "http://" + path.Join(JENKINS_SERVER, "job", nameAndId, "artifact", info.Artifacts[artifact])
	return getRemote(url)
}

func GetArtifacts(name string, id int, output string) ([]string, error) {
	log.Print("Fetching ", name, " to ", output)
	id, err := sanitizeId(name, id)
	if err != nil {
		return nil, err
	}
	info, err := GetBuildInfo(name, id)
	if err != nil {
		return nil, err
	}
	if info.Result != "SUCCESS" {
		return nil, errors.New("the build you requested failed")
	}
	nameAndId := path.Join(name, strconv.Itoa(id))
	artifacts := []string{}
	log.Print("Fetching artifacts for build #", id, " (", len(info.Artifacts), " total)")
	for outpath, inpath := range info.Artifacts {
		url := "http://" + path.Join(JENKINS_SERVER, "job", nameAndId, "artifact", inpath)
		artifact, err := getRemote(url)
		if err != nil {
			return artifacts, err
		}
		defer artifact.Close()

		dir := path.Join(output, path.Dir(outpath))
		errMkdir := os.MkdirAll(dir, os.ModeDir|0755)
		if errMkdir != nil {
			return artifacts, errMkdir
		}
		fo, errFo := os.Create(path.Join(output, outpath))
		if errFo != nil {
			return artifacts, errFo
		}
		defer fo.Close()
		log.Print("-> ", path.Join(output, outpath))
		io.Copy(fo, artifact)
	}
	return artifacts, nil
}

func GetBuildInfo(name string, id int) (*JenkinsBuildInfo, error) {
	id, err := sanitizeId(name, id)
	if err != nil {
		return nil, err
	}
	json, err := get(name, id)
	if err != nil || json == nil {
		return nil, err
	}
	info := JenkinsBuildInfo{}
	info.Name = json["fullDisplayName"].(string)
	info.Id = int(json["number"].(float64))
	artifacts := json["artifacts"].([]interface{})
	info.Artifacts = make(map[string]string, 10)
	for _, artifact := range artifacts {
		artifactSafe := artifact.(map[string]interface{})
		info.Artifacts[artifactSafe["displayPath"].(string)] = artifactSafe["relativePath"].(string)
	}
	info.Building = json["building"].(bool)
	info.Duration = json["duration"].(float64)
	info.EstimatedDuration = json["estimatedDuration"].(float64)
	if json["result"] != nil {
		info.Result = json["result"].(string)
	} else {
		info.Result = "BUILDING"
	}
	info.Timestamp = json["timestamp"].(float64)
	info.Url = json["url"].(string)
	return &info, nil
}

func GetInfo(name string) (*JenkinsInfo, error) {
	json, err := get(name, -1)
	if err != nil || json == nil {
		return nil, err
	}
	info := JenkinsInfo{}
	info.Name = json["name"].(string)
	info.Description = json["description"].(string)
	info.Url = json["url"].(string)
	info.Buildable = json["buildable"].(bool)
	info.InQueue = json["inQueue"].(bool)
	lastBuild := json["lastBuild"]
	if lastBuild != nil {
		lastBuildSafe := lastBuild.(map[string]interface{})
		info.LastBuild = int(lastBuildSafe["number"].(float64))
		info.LastBuildUrl = lastBuildSafe["url"].(string)
	}
	lastStableBuild := json["lastStableBuild"]
	if lastStableBuild != nil {
		lastStableBuildSafe := lastStableBuild.(map[string]interface{})
		info.LastStableBuild = int(lastStableBuildSafe["number"].(float64))
		info.LastStableBuildUrl = lastStableBuildSafe["url"].(string)
	}
	return &info, nil
}
