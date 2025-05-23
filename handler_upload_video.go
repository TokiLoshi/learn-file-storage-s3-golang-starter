package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	// 2 - extract videoId from url path parameters and parse as UUID
	videoIDString := r.PathValue("videoID")
	fmt.Println("Request url: ", r.URL.Path)
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}
	fmt.Printf("Video ID: %v", videoID)

		// 3 - AUthenticate user to get userID
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return 
	}

	// 1 - Set an upload limit of 1GB (1 << 30 bytes using http.MaxBytesReader)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	const maxMemory = 32 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, 500, "error passing max memory", err)
		return 
	}

	// 5 - Parse loaded vido from form data
	// Use http.Request.FormFile with key video to get multipart.File in memory
	// defer closing  the file with os.File.Close
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()


	// 6 - validate the upladed file to ensure it's mb4 
	// use mime.ParseMediaType and "video/mp4" as MIME type
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Unable to get mediaType", nil)
		return
	}

	mediaContentType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Media Type", nil)
		return
	}

	if mediaContentType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Content is not an image", nil)
		return
	}

 
	// 4 - Get video metadata from database
	// if user is not video owner return http.StatusUnauthoized
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unablle to get video from db", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", fmt.Errorf("unauthorized"))
		return
	}


	// 7 - Save the uploaded file as a temporay file on dis 
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temporary file: %v", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save temp file: %v", err)
		return
	}


	// 8 - Reset tempFiles file pointer to begenning with .Seek(0, io.SeekStart) to allow us to read file from beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't reset the temp file: %v", err)
		return
	}

	fileName := tempFile.Name()
	fmt.Printf("Filename: %v", fileName)

	aspectRatio, err := getVideoAspectRatio(fileName)
	fmt.Printf("Aspect ratio: %v", aspectRatio)

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random bytes: %v", err)
		return
	}

	orientation := "other"
	if aspectRatio == "16:9" {
		orientation = "landscape"
	} else if aspectRatio == "9:16" {
		orientation = "portrait"
	}
	
	// 9 - Put object into S3 using PutObject 
	randomBytes := make([]byte, 16)
	_, err = rand.Read(randomBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random bytes: %v", err)
		return
	}
	fileKey := fmt.Sprintf("%v/%x.mp4", orientation, randomBytes)
	
	putObject := &s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key: &fileKey,
		Body: tempFile,
		ContentType: &mediaContentType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), putObject)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldnt upload file to s3: %v", err)
		return
	}
	
	// 10 - Update the VideoURL of video record in database with s3 bucket and key
	// S3 buckets https://<bucket-name>.s3.<region>.amazonaws.com/<key>
	// Ensure you use correct region and bucket name 
	s3URL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileKey)

	video.VideoURL = &s3URL
	err = cfg.db.UpdateVideo(video) 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video url: %v", err)
		return
	}

	respondWithJSON(w, http.StatusOK, struct{}{})
	// 11 - restart server and test handler by uploading boots-video-vertical.mp4
	// ensure video is uploaded to s3 bucket with key and shows up in webUI
}

func getVideoAspectRatio(filePath string) (string, error) {
	type streams struct {
		Width int `json:"width"`
		Height int `json:"height"`
	}

	type FFProbeOutput struct {
		Streams []streams `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	var buffer bytes.Buffer

	cmd.Stdout = &buffer 

	err := cmd.Run()
	if err != nil {
		log.Fatalf("Error running command!: %v", err)
	}

	fmt.Printf("Buffer: %v", buffer)

	var output FFProbeOutput 

	err = json.Unmarshal(buffer.Bytes(), &output)
	if err != nil {
		log.Fatalf("Error unmarshalling json data: %v", err)
	}

	width := output.Streams[0].Width 
	height := output.Streams[0].Height

	aspectRatio := float64(width) / float64(height)
	
	heightByWidth := math.Abs(aspectRatio -(16.0 / 9.0))
	if heightByWidth < 0.01 {
		return "16:9", nil
	}
	widthByHeight := math.Abs(aspectRatio -(9.0 / 16.0))
	if widthByHeight < 0.01 {
		return "9:16", nil
	}
	return "other", nil
}