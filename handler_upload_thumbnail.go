package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

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


	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	const maxMemory = 10 << 20 
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, 500, "error passing max memory", err)
		return
	}

	// use the image data from the form 
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

		// Read all the image data into a byte slice using io.ReadAll 
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Unable to get mediaType", fmt.Errorf("no content type"))
		return
	}

	imageData, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to parse form file", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video from db", err)
		return
	}

		// if the authenticated user is not the video owner return htttp.StatusUnauthorized response
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unathorized", fmt.Errorf("unauthorized"))	
		return
	}

	fields := strings.Split(mediaType, "/")
	imageType := fmt.Sprintf(`%s`, fields[1]) 

	// path shape: /assets/videoId.fileExtension 
	fileExtension := fmt.Sprintf(`.%s`, imageType)
	fmt.Printf(`File extension: %s\n`, fileExtension)
	
	mediaPath := filepath.Join(cfg.assetsRoot, videoIDString + fileExtension)
	fmt.Printf(`Media path: %s.\n`, mediaPath)
	destFile, error := os.Create(mediaPath)
	
	if error != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get create file", err)
		return
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, bytes.NewReader(imageData))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to copy file", err)
	}

	url := fmt.Sprintf(`/assets/%s%s`, videoIDString, fileExtension) 
	video.ThumbnailURL = &url
	
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	} 

	respondWithJSON(w, http.StatusOK, video)
}
