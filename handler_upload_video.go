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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	// 2 - extract videoId from url path parameters and parse as UUID
	videoIDString := r.PathValue("videoID")
	fmt.Println("Request url: ", r.URL.Path)
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID\n", err)
		return
	}
	fmt.Printf("Video ID: %v", videoID)

		// 3 - AUthenticate user to get userID
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT\n", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT\n", err)
		return 
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	const maxMemory = 32 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, 500, "error passing max memory\n", err)
		return 
	}

	// Parse loaded vido from form data
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file\n", err)
		return
	}
	defer file.Close()


	// validate the upladed file to ensure it's mp4 
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Unable to get mediaType\n", nil)
		return
	}

	mediaContentType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Media Type\n", nil)
		return
	}

	if mediaContentType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Content is not an image\n", nil)
		return
	}

 
	// Get video metadata from database
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unablle to get video from db\n", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", fmt.Errorf("unauthorized"))
		return
	}

	// Save the uploaded file as a temporay file on disk
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temporary file: %v\n", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save temp file: %v\n", err)
		return
	}

	// Reset tempFiles file pointer to begenning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't reset the temp file: %v\n", err)
		return
	}
	

	// Process the video for faster starts
	fileName, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to process video mov atom: %v\n", err)
	}

	// Get the aspect ratio to store in the bucket 
	aspectRatio, err := getVideoAspectRatio(fileName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random bytes: %v\n", err)
		return
	}
	orientation := "other"
	if aspectRatio == "16:9" {
		orientation = "landscape"
	} else if aspectRatio == "9:16" {
		orientation = "portrait"
	}

	// Open the file to get a pointer to store 
	processedFile, err := os.Open(fileName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed file: %v\n", err)
	}
	defer processedFile.Close()
	
	// Put object into S3 using PutObject 
	randomBytes := make([]byte, 16)
	_, err = rand.Read(randomBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random bytes: %v\n", err)
		return
	}
	fileKey := fmt.Sprintf("%v/%x.mp4", orientation, randomBytes)
	
	putObject := &s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key: &fileKey,
		Body: processedFile,
		ContentType: &mediaContentType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), putObject)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldnt upload file to s3: %v\n", err)
		return
	}
	
	// Update the VideoURL of video record in database with s3 bucket and key
	// s3URL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileKey)
	s3URL := fmt.Sprintf("%s,%s", cfg.s3Bucket, fileKey)

	video.VideoURL = &s3URL

	err = cfg.db.UpdateVideo(video) 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video url: %v\n", err)
		return
	}

	presigned, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate presign URL: %v\n", err)
		return
	}

	respondWithJSON(w, http.StatusOK, presigned)
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
		log.Fatalf("Error running command!: %v\n", err)
	}

	var output FFProbeOutput 

	err = json.Unmarshal(buffer.Bytes(), &output)
	if err != nil {
		log.Fatalf("Error unmarshalling json data: %v\n", err)
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

func processVideoForFastStart(filePath string) (string, error) {
	// Create a new string for output path (append .process to input)
		// this should be the path to the temp file on disk 
	outputPath := filePath + ".processing"
		// Create a new exec.Cmd command 
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	fmt.Printf("Command: %v", cmd) 

	// Run the command 
	err := cmd.Run()
	if err != nil {
		log.Fatalf("Error running command: %v\n", err)
		return "", err
	}
	// return the output file path 
	fmt.Printf("Output path being returned %v\n", outputPath)
	return outputPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	// Use the SDK to create a s3.PresignClient 
	presignClient := s3.NewPresignClient(s3Client)
	presignResult, err := presignClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key: aws.String(key), 
	}, s3.WithPresignExpires(expireTime))
	if err != nil { 
		return "", err
	}
	return presignResult.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	url := *video.VideoURL
	details := strings.Split(url, ",")
	if len(details) != 2 {
		return video, nil
	}
	bucket := details[0] 
	key := details[1]
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 15*time.Minute)
	if err != nil {
		return video, err
	}
	video.VideoURL = &presignedURL
	return video, nil
}