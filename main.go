package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// TrackInfo stores details of each audio track found in the input file.
type TrackInfo struct {
	Index    string // Index of the track within the file
	Layout   string // Audio channel layout (e.g., "5.1", "7.1")
	Language string // Language of the audio track
	Title    string // Title of the track, if available
}

func main() {
	// Check command line arguments for input file
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run script.go <input.mkv>")
		os.Exit(1)
	}

	inputFile := os.Args[1]
	outputFile := strings.TrimSuffix(inputFile, ".mkv") + "_enhanced.mkv"

	// Extract track information from the input file
	trackInfos, err := extractTrackInfo(inputFile)
	if err != nil {
		fmt.Println("Error extracting track info:", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	for _, track := range trackInfos {
		wg.Add(1)
		go processTrack(inputFile, track, &wg)
	}
	wg.Wait()

	// Merge the processed tracks back into a single MKV file
	if err := mergeTracks(inputFile, outputFile, trackInfos); err != nil {
		fmt.Println("Error merging tracks:", err)
		os.Exit(1)
	}

	removeTemporaryFiles(inputFile, trackInfos)

	fmt.Println("Enhanced MKV generated:", outputFile)
}

// extractTrackInfo uses ffprobe to extract audio track details from a video file.
func extractTrackInfo(file string) ([]TrackInfo, error) {
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return nil, fmt.Errorf("file does not exist: %s", file)
	}

	// Use ffprobe to get audio track information
	cmd := exec.Command("ffprobe", "-loglevel", "error", "-select_streams", "a",
		"-show_entries", "stream=index,channel_layout:stream_tags=language,title",
		"-of", "compact=p=0:nk=1", file)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed with error: %s\nOutput: %s", err, string(output))
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	var tracks []TrackInfo
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "|")
		if len(parts) >= 3 {
			track := TrackInfo{
				Index:    parts[0],
				Layout:   parts[1],
				Language: parts[2],
				Title:    "", // Default empty if not provided
			}
			if len(parts) > 3 {
				track.Title = parts[3]
			}
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}

// processTrack processes each audio track individually using ffmpeg.
func processTrack(inputFile string, track TrackInfo, wg *sync.WaitGroup) {
	defer wg.Done()

	// Define audio filters based on the channel layout
	var af string
	if strings.HasPrefix(track.Layout, "7.1") {
		af = "volume=1.5, pan=stereo|FL=FL+0.707*FC+0.5*BL+0.3*SL+0.5*LFE|FR=FR+0.707*FC+0.5*BR+0.3*SR+0.5*LFE"
	} else {
		af = "volume=1.5, pan=stereo|FL=FL+0.707*FC+0.707*BL+0.5*LFE|FR=FR+0.707*FC+0.707*BR+0.5*LFE"
	}
	enhancedFile := strings.TrimSuffix(inputFile, ".mkv") + "_track" + track.Index + "_enhanced.opus"

	// Skip processing if enhanced track already exists
	if _, err := os.Stat(enhancedFile); err == nil {
		fmt.Printf("Enhanced track %s already exists, skipping processing\n", track.Index)
		return
	}

	cmd := exec.Command("ffmpeg",
		"-i", inputFile,
		"-map", "0:"+track.Index,
		"-af", af,
		"-acodec", "libopus", "-b:a", "320k",
		"-vbr", "on",
		"-compression_level", "9",
		"-frame_duration", "20",
		"-application", "audio",
		"-metadata:s:a", "language="+track.Language,
		"-metadata:s:a", "title=2.1 Enhanced",
		"-y", enhancedFile)

	// Execute the ffmpeg command and capture stderr for error tracking
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("Error starting ffmpeg for track %s: %v\n", track.Index, err)
		return
	}

	// Print ffmpeg output in real time
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			fmt.Println("FFmpeg Output:", scanner.Text())
		}
	}()

	if err := cmd.Wait(); err != nil {
		fmt.Printf("FFmpeg command for track %s failed: %v\n", track.Index, err)
	}
}

// mergeTracks combines video, original audio, and enhanced audio tracks into a single file.
func mergeTracks(inputFile, outputFile string, tracks []TrackInfo) error {
	args := []string{"-i", inputFile} // Include the original video file

	for _, track := range tracks {
		enhancedFile := strings.TrimSuffix(inputFile, ".mkv") + "_track" + track.Index + "_enhanced.opus"
		args = append(args, "-i", enhancedFile) // Include enhanced audio tracks
	}

	args = append(args, "-map", "0:v")  // Map video stream from the original file
	args = append(args, "-map", "0:s?") // Map subtitle streams, if available

	// Copy original and enhanced audio streams
	for i := range tracks {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", i), "-c:a", "copy")
		args = append(args, "-map", fmt.Sprintf("%d:a", 1+i), "-c:a", "copy")
	}

	args = append(args, "-c:v", "copy", "-c:s", "copy", "-y", outputFile)

	// Debugging: Print the ffmpeg command to verify correctness
	fmt.Println("ffmpeg", strings.Join(args, " "))

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg command failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return nil
}

// removeTemporaryFiles deletes all temporary enhanced audio files.
func removeTemporaryFiles(inputFile string, tracks []TrackInfo) error {
	for _, track := range tracks {
		// Construct the filename for each temporary enhanced audio file
		enhancedFile := strings.TrimSuffix(inputFile, ".mkv") + "_track" + track.Index + "_enhanced.opus"
		// Remove the file
		err := os.Remove(enhancedFile)
		if err != nil {
			fmt.Printf("Failed to delete temporary file %s: %v\n", enhancedFile, err)
			return err
		}
		fmt.Printf("Temporary file %s removed successfully.\n", enhancedFile)
	}
	return nil
}
