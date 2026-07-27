package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type VideoRequest struct {
	VideoPath string `json:"video_path"`
	OutputDir string `json:"output_dir"`
}

type ProcessingResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	ZipPath    string   `json:"zip_path,omitempty"`
	FrameCount int      `json:"frame_count,omitempty"`
	Images     []string `json:"images,omitempty"`
}

func main() {
	createDirs()

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	r.Static("/uploads", "./uploads")
	r.Static("/outputs", "./outputs")

	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html")
		c.String(200, getHTMLForm())
	})

	r.POST("/upload", handleVideoUpload)

	r.GET("/download/:filename", handleDownload)

	r.GET("/api/status", handleStatus)

	fmt.Println("🎬 Servidor iniciado na porta 8080")
	fmt.Println("📂 Acesse: http://localhost:8080")

	log.Fatal(r.Run(":8080"))
}

func createDirs() {
	dirs := []string{"uploads", "outputs", "temp"}
	for _, dir := range dirs {
		os.MkdirAll(dir, 0755)
	}
}

func handleVideoUpload(c *gin.Context) {
	file, header, err := c.Request.FormFile("video")
	if err != nil {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Erro ao receber arquivo: " + err.Error(),
		})
		return
	}
	defer file.Close()

	if !isValidVideoFile(header.Filename) {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Formato de arquivo não suportado. Use: mp4, avi, mov, mkv",
		})
		return
	}

	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s", timestamp, header.Filename)
	videoPath := filepath.Join("uploads", filename)

	out, err := os.Create(videoPath)
	if err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}

	result := processVideo(videoPath, timestamp)

	if result.Success {
		os.Remove(videoPath)
	}

	c.JSON(200, result)
}

func processVideo(videoPath, timestamp string) ProcessingResult {
	fmt.Printf("Iniciando processamento: %s\n", videoPath)

	tempDir := filepath.Join("temp", timestamp)
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	framePattern := filepath.Join(tempDir, "frame_%04d.png")

	cmd := exec.Command("ffmpeg",
		"-i", videoPath,
		"-vf", "fps=1",
		"-y",
		framePattern,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ProcessingResult{
			Success: false,
			Message: fmt.Sprintf("Erro no ffmpeg: %s\nOutput: %s", err.Error(), string(output)),
		}
	}

	frames, err := filepath.Glob(filepath.Join(tempDir, "*.png"))
	if err != nil || len(frames) == 0 {
		return ProcessingResult{
			Success: false,
			Message: "Nenhum frame foi extraído do vídeo",
		}
	}

	fmt.Printf("📸 Extraídos %d frames\n", len(frames))

	zipFilename := fmt.Sprintf("frames_%s.zip", timestamp)
	zipPath := filepath.Join("outputs", zipFilename)

	err = createZipFile(frames, zipPath)
	if err != nil {
		return ProcessingResult{
			Success: false,
			Message: "Erro ao criar arquivo ZIP: " + err.Error(),
		}
	}

	fmt.Printf("✅ ZIP criado: %s\n", zipPath)

	imageNames := make([]string, len(frames))
	for i, frame := range frames {
		imageNames[i] = filepath.Base(frame)
	}

	return ProcessingResult{
		Success:    true,
		Message:    fmt.Sprintf("Processamento concluído! %d frames extraídos.", len(frames)),
		ZipPath:    zipFilename,
		FrameCount: len(frames),
		Images:     imageNames,
	}
}

func createZipFile(files []string, zipPath string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for _, file := range files {
		err := addFileToZip(zipWriter, file)
		if err != nil {
			return err
		}
	}

	return nil
}

func addFileToZip(zipWriter *zip.Writer, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	header.Name = filepath.Base(filename)
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
}

func handleDownload(c *gin.Context) {
	filename := c.Param("filename")
	filePath := filepath.Join("outputs", filename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(404, gin.H{"error": "Arquivo não encontrado"})
		return
	}

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/zip")

	c.File(filePath)
}

func handleStatus(c *gin.Context) {
	files, err := filepath.Glob(filepath.Join("outputs", "*.zip"))
	if err != nil {
		c.JSON(500, gin.H{"error": "Erro ao listar arquivos"})
		return
	}

	var results []map[string]interface{}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		results = append(results, map[string]interface{}{
			"filename":     filepath.Base(file),
			"size":         info.Size(),
			"created_at":   info.ModTime().Format("2006-01-02 15:04:05"),
			"download_url": "/download/" + filepath.Base(file),
		})
	}

	c.JSON(200, gin.H{
		"files": results,
		"total": len(results),
	})
}

func isValidVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	validExts := []string{".mp4", ".avi", ".mov", ".mkv", ".wmv", ".flv", ".webm"}

	for _, validExt := range validExts {
		if ext == validExt {
			return true
		}
	}
	return false
}

func getHTMLForm() string {
	return `
<!DOCTYPE html>
<html lang="pt-BR">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>FIAP X - Processador de Vídeos</title>
    <style>
        body { 
            font-family: Arial, sans-serif; 
            max-width: 800px; 
            margin: 50px auto; 
            padding: 20px;
            background-color: #f5f5f5;
        }
        .container {
            background: white;
            padding: 30px;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        h1 { 
            color: #333; 
            text-align: center;
            margin-bottom: 30px;
        }
        .upload-form {
            border: 2px dashed #ddd;
            padding: 30px;
            text-align: center;
            border-radius: 10px;
            margin: 20px 0;
        }
        input[type="file"] {
            margin: 20px 0;
            padding: 10px;
        }
        button {
            background: #007bff;
            color: white;
            padding: 12px 30px;
            border: none;
            border-radius: 5px;
            cursor: pointer;
            font-size: 16px;
        }
        button:hover { background: #0056b3; }
        .result {
            margin-top: 20px;
            padding: 15px;
            border-radius: 5px;
            display: none;
        }
        .success { background: #d4edda; color: #155724; border: 1px solid #c3e6cb; }
        .error { background: #f8d7da; color: #721c24; border: 1px solid #f5c6cb; }
        .loading { 
            text-align: center; 
            display: none;
            margin: 20px 0;
        }
        .files-list {
            margin-top: 30px;
        }
        .file-item {
            background: #f8f9fa;
            padding: 10px;
            margin: 5px 0;
            border-radius: 5px;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .download-btn {
            background: #28a745;
            color: white;
            padding: 5px 15px;
            text-decoration: none;
            border-radius: 3px;
            font-size: 14px;
        }
        .download-btn:hover { background: #218838; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎬 FIAP X - Processador de Vídeos</h1>
        <p style="text-align: center; color: #666;">
            Faça upload de um vídeo e receba um ZIP com todos os frames extraídos!
        </p>
        
        <form id="uploadForm" class="upload-form">
            <p><strong>Selecione um arquivo de vídeo:</strong></p>
            <input type="file" id="videoFile" accept="video/*" required>
            <br>
            <button type="submit">🚀 Processar Vídeo</button>
        </form>
        
        <div class="loading" id="loading">
            <p>⏳ Processando vídeo... Isso pode levar alguns minutos.</p>
        </div>
        
        <div class="result" id="result"></div>
        
        <div class="files-list">
            <h3>📁 Arquivos Processados:</h3>
            <div id="filesList">Carregando...</div>
        </div>
    </div>

    <script>
        document.getElementById('uploadForm').addEventListener('submit', async function(e) {
            e.preventDefault();
            
            const fileInput = document.getElementById('videoFile');
            const file = fileInput.files[0];
            
            if (!file) {
                showResult('Selecione um arquivo de vídeo!', 'error');
                return;
            }
            
            const formData = new FormData();
            formData.append('video', file);
            
            showLoading(true);
            hideResult();
            
            try {
                const response = await fetch('/upload', {
                    method: 'POST',
                    body: formData
                });
                
                const result = await response.json();
                
                if (result.success) {
                    showResult(
                        result.message + 
                        '<br><br><a href="/download/' + result.zip_path + '" class="download-btn">⬇️ Download ZIP</a>',
                        'success'
                    );
                    loadFilesList();
                } else {
                    showResult('Erro: ' + result.message, 'error');
                }
            } catch (error) {
                showResult('Erro de conexão: ' + error.message, 'error');
            } finally {
                showLoading(false);
            }
        });
        
        function showResult(message, type) {
            const result = document.getElementById('result');
            result.innerHTML = message;
            result.className = 'result ' + type;
            result.style.display = 'block';
        }
        
        function hideResult() {
            document.getElementById('result').style.display = 'none';
        }
        
        function showLoading(show) {
            document.getElementById('loading').style.display = show ? 'block' : 'none';
        }
        
        async function loadFilesList() {
            try {
                const response = await fetch('/api/status');
                const data = await response.json();
                
                const filesList = document.getElementById('filesList');
                
                if (data.files && data.files.length > 0) {
                    filesList.innerHTML = data.files.map(file => 
                        '<div class="file-item">' +
                        '<span>' + file.filename + ' (' + formatFileSize(file.size) + ') - ' + file.created_at + '</span>' +
                        '<a href="' + file.download_url + '" class="download-btn">⬇️ Download</a>' +
                        '</div>'
                    ).join('');
                } else {
                    filesList.innerHTML = '<p>Nenhum arquivo processado ainda.</p>';
                }
            } catch (error) {
                document.getElementById('filesList').innerHTML = '<p>Erro ao carregar arquivos.</p>';
            }
        }
        
        function formatFileSize(bytes) {
            if (bytes === 0) return '0 Bytes';
            const k = 1024;
            const sizes = ['Bytes', 'KB', 'MB', 'GB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
        }
        
        // Carregar lista de arquivos ao inicializar
        loadFilesList();
    </script>
</body>
</html>`
}
