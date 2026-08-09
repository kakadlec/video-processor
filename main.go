package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

	identity, _, err := setupIdentity(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	r := setupRouterWithIdentity(identity)

	fmt.Println("🎬 Servidor iniciado na porta 8080")
	fmt.Println("📂 Acesse: http://localhost:8080")

	log.Fatal(r.Run(":8080"))
}

func setupRouterWithIdentity(identity *identityModule) *gin.Engine {
	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html")
		c.String(200, getHTMLForm())
	})

	// videoRoutes holds every route that serves or accepts video-processing
	// artifacts. All of them require a valid bearer token.
	videoRoutes := r.Group("/")
	videoRoutes.Use(identity.requireBearerAuth())

	// The /uploads and /outputs static mounts serve the exact same artifacts
	// as handleDownload by filename, so they need the same per-owner check —
	// otherwise they'd be a direct bypass of handleDownload's ownership
	// enforcement below. rejectOwnerSidecarRequests runs unconditionally: the
	// sidecar files themselves must never be servable, including any left
	// over from before this route was protected.
	uploadsRoutes := videoRoutes.Group("/uploads")
	uploadsRoutes.Use(rejectOwnerSidecarRequests())
	uploadsRoutes.Use(requireArtifactOwnership("uploads"))
	uploadsRoutes.Static("/", "./uploads")

	outputsRoutes := videoRoutes.Group("/outputs")
	outputsRoutes.Use(rejectOwnerSidecarRequests())
	outputsRoutes.Use(requireArtifactOwnership("outputs"))
	outputsRoutes.Static("/", "./outputs")

	videoRoutes.POST("/upload", handleVideoUpload)
	videoRoutes.GET("/download/:filename", handleDownload)
	videoRoutes.GET("/api/status", handleStatus)

	identity.registerRoutes(r)

	return r
}

// artifactOwnerSuffix names the sidecar file that records which
// authenticated user owns a video-processing artifact (an upload or an
// output zip), stored alongside it under the same directory. This is
// explicit, checked ownership metadata — not an inference from the
// artifact's filename or timestamp.
const artifactOwnerSuffix = ".owner"

// recordArtifactOwner persists that userID owns the artifact at
// filepath.Join(dir, artifactFilename). dir is confined to like videoPath
// and zipPath are confined elsewhere in this file.
func recordArtifactOwner(dir, artifactFilename, userID string) error {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid artifact filename: %s", artifactFilename)
	}
	return os.WriteFile(path, []byte(userID), 0600)
}

// artifactOwner returns the userID recorded by recordArtifactOwner for the
// artifact at filepath.Join(dir, artifactFilename), if any.
func artifactOwner(dir, artifactFilename string) (string, bool) {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// removeArtifactOwner deletes the ownership sidecar for the artifact at
// filepath.Join(dir, artifactFilename), confined the same way as above.
func removeArtifactOwner(dir, artifactFilename string) error {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid artifact filename: %s", artifactFilename)
	}
	return os.Remove(path)
}

// rejectOwnerSidecarRequests blocks direct HTTP access to ownership sidecar
// files under a static file group. It's registered unconditionally,
// regardless of whether identity is configured, since a sidecar written
// during an earlier, identity-enabled run of the server must stay
// unreadable even after identity is turned off.
func rejectOwnerSidecarRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		filename := filepath.Base(c.Param("filepath"))
		if strings.HasSuffix(filename, artifactOwnerSuffix) {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}
		c.Next()
	}
}

// requireArtifactOwnership gates a static file group (uploads/ or outputs/)
// so an authenticated user can only fetch artifacts recorded as their own.
// It's only registered behind requireBearerAuth, so an authenticated UserID
// is always present in context by the time it runs.
func requireArtifactOwnership(dir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := authenticatedUserID(c)
		if !ok {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}

		filename := filepath.Base(c.Param("filepath"))
		owner, hasOwner := artifactOwner(dir, filename)
		if !hasOwner || owner != userID.String() {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}

		c.Next()
	}
}

func createDirs() {
	dirs := []string{"uploads", "outputs", "temp"}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Printf("Failed to create directory %s: %v", dir, err)
		}
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

	// requestID must be collision-resistant, not just distinct-looking: it
	// names the temp dir, the output zip, and (when authenticated) the
	// ownership sidecar for both. A colliding ID from two concurrent
	// requests would let one user's upload overwrite another's artifact and
	// its ownership record — a UUID keeps that probability negligible,
	// unlike the second-precision timestamp this used to be.
	requestID := uuid.NewString()
	safeFilename := filepath.Base(header.Filename)
	filename := fmt.Sprintf("%s_%s", requestID, safeFilename)
	videoPath := filepath.Clean(filepath.Join("uploads", filename))
	if !strings.HasPrefix(videoPath, "uploads"+string(os.PathSeparator)) {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Nome de arquivo inválido",
		})
		return
	}

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

	userID, authenticated := authenticatedUserID(c)
	if authenticated {
		if err := recordArtifactOwner("uploads", filename, userID.String()); err != nil {
			log.Printf("Failed to record owner for upload %s: %v", filename, err)
		}
	}

	result := processVideo(videoPath, requestID)

	if result.Success {
		if err := os.Remove(videoPath); err != nil {
			log.Printf("Failed to remove original upload %s: %v", videoPath, err)
		}
		if authenticated {
			if err := removeArtifactOwner("uploads", filename); err != nil {
				log.Printf("Failed to remove owner record for upload %s: %v", filename, err)
			}
			// If ownership can't be recorded, the zip would otherwise be
			// reported as a success the owner can never actually retrieve
			// (every ownership-checked read path fails closed on a missing
			// sidecar). Treat it as a processing failure instead.
			if err := recordArtifactOwner("outputs", result.ZipPath, userID.String()); err != nil {
				log.Printf("Failed to record owner for output %s: %v", result.ZipPath, err)
				if removeErr := os.Remove(filepath.Join("outputs", result.ZipPath)); removeErr != nil {
					log.Printf("Failed to remove orphaned output %s: %v", result.ZipPath, removeErr)
				}
				c.JSON(500, ProcessingResult{
					Success: false,
					Message: "Failed to record artifact ownership",
				})
				return
			}
		}
	}

	c.JSON(200, result)
}

func processVideo(videoPath, requestID string) ProcessingResult {
	fmt.Printf("Iniciando processamento: %s\n", videoPath)

	tempDir := filepath.Join("temp", requestID)
	if err := os.MkdirAll(tempDir, 0750); err != nil {
		log.Printf("Failed to create temp directory %s: %v", tempDir, err)
	}
	defer os.RemoveAll(tempDir)

	framePattern := filepath.Join(tempDir, "frame_%04d.png")

	cmd := exec.Command("ffmpeg", // #nosec G204
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

	zipFilename := fmt.Sprintf("frames_%s.zip", requestID)
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
	zipPath = filepath.Clean(zipPath)
	if !strings.HasPrefix(zipPath, "outputs"+string(os.PathSeparator)) {
		return fmt.Errorf("invalid zip path: %s", zipPath)
	}

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
	filename = filepath.Clean(filename)
	if !strings.HasPrefix(filename, "temp"+string(os.PathSeparator)) {
		return fmt.Errorf("invalid frame path: %s", filename)
	}

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

	// The not-found and not-owned responses below are deliberately
	// identical: a non-owner must not be able to tell "doesn't exist" apart
	// from "exists but isn't yours" by response content.
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(404, gin.H{"error": "File not found"})
		return
	}

	if userID, authenticated := authenticatedUserID(c); authenticated {
		owner, hasOwner := artifactOwner("outputs", filename)
		if !hasOwner || owner != userID.String() {
			c.JSON(404, gin.H{"error": "File not found"})
			return
		}
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

	userID, authenticated := authenticatedUserID(c)

	var results []map[string]interface{}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		if authenticated {
			owner, hasOwner := artifactOwner("outputs", filepath.Base(file))
			if !hasOwner || owner != userID.String() {
				continue
			}
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
            border: none;
            cursor: pointer;
        }
        .download-btn:hover { background: #218838; }
        .auth-panel {
            border: 1px solid #ddd;
            padding: 20px;
            border-radius: 10px;
            margin-bottom: 20px;
        }
        .auth-panel input {
            padding: 8px;
            margin: 5px;
            border: 1px solid #ccc;
            border-radius: 4px;
        }
        .auth-panel button {
            padding: 8px 20px;
            font-size: 14px;
            margin: 5px;
        }
        .auth-message {
            margin-top: 10px;
            font-size: 14px;
        }
        .auth-message.error { color: #721c24; }
        .auth-logged-in {
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎬 FIAP X - Processador de Vídeos</h1>
        <p style="text-align: center; color: #666;">
            Faça upload de um vídeo e receba um ZIP com todos os frames extraídos!
        </p>

        <div class="auth-panel" id="authPanel">
            <div id="authForms">
                <label for="authEmail">Email:</label>
                <input type="email" id="authEmail" placeholder="Email">
                <label for="authPassword">Senha:</label>
                <input type="password" id="authPassword" placeholder="Senha">
                <button type="button" id="loginBtn">🔑 Entrar</button>
                <button type="button" id="registerBtn">📝 Cadastrar</button>
            </div>
            <div class="auth-logged-in" id="authLoggedIn" style="display: none;">
                <span id="authEmailDisplay"></span>
                <button type="button" id="logoutBtn">🚪 Sair</button>
            </div>
            <div class="auth-message" id="authMessage"></div>
        </div>

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
        const ACCESS_TOKEN_KEY = 'fiapx_access_token';
        const ACCOUNT_EMAIL_KEY = 'fiapx_account_email';

        function getAccessToken() {
            return localStorage.getItem(ACCESS_TOKEN_KEY);
        }

        function setSession(accessToken, email) {
            localStorage.setItem(ACCESS_TOKEN_KEY, accessToken);
            localStorage.setItem(ACCOUNT_EMAIL_KEY, email);
            updateAuthUI();
        }

        function clearSession() {
            localStorage.removeItem(ACCESS_TOKEN_KEY);
            localStorage.removeItem(ACCOUNT_EMAIL_KEY);
            updateAuthUI();
        }

        function authHeaders() {
            const token = getAccessToken();
            return token ? { 'Authorization': 'Bearer ' + token } : {};
        }

        function updateAuthUI() {
            const token = getAccessToken();
            document.getElementById('authForms').style.display = token ? 'none' : 'block';
            document.getElementById('authLoggedIn').style.display = token ? 'flex' : 'none';
            if (token) {
                document.getElementById('authEmailDisplay').textContent =
                    'Autenticado como ' + localStorage.getItem(ACCOUNT_EMAIL_KEY);
            }
        }

        function showAuthMessage(message, type) {
            const el = document.getElementById('authMessage');
            el.textContent = message;
            el.className = 'auth-message' + (type ? ' ' + type : '');
        }

        async function submitAuth(path) {
            const email = document.getElementById('authEmail').value;
            const password = document.getElementById('authPassword').value;
            try {
                const response = await fetch(path, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ email: email, password: password })
                });
                const data = await response.json().catch(function() { return {}; });
                if (!response.ok) {
                    showAuthMessage(data.error || 'Falha na autenticação.', 'error');
                    return;
                }
                if (path === '/api/auth/register') {
                    showAuthMessage('Cadastro realizado! Entrando...', '');
                    await submitAuth('/api/auth/login');
                    return;
                }
                setSession(data.access_token, email);
                showAuthMessage('');
                loadFilesList();
            } catch (error) {
                showAuthMessage('Erro de conexão: ' + error.message, 'error');
            }
        }

        document.getElementById('loginBtn').addEventListener('click', function() {
            submitAuth('/api/auth/login');
        });

        document.getElementById('registerBtn').addEventListener('click', function() {
            submitAuth('/api/auth/register');
        });

        document.getElementById('logoutBtn').addEventListener('click', function() {
            clearSession();
            loadFilesList();
        });

        async function downloadFile(filename) {
            try {
                const response = await fetch('/download/' + encodeURIComponent(filename), {
                    headers: authHeaders()
                });
                if (response.status === 401) {
                    clearSession();
                    showResult('Sessão expirada. Faça login novamente.', 'error');
                    return;
                }
                if (!response.ok) {
                    showResult('Erro ao baixar o arquivo.', 'error');
                    return;
                }
                const blob = await response.blob();
                const url = URL.createObjectURL(blob);
                const link = document.createElement('a');
                link.href = url;
                link.download = filename;
                document.body.appendChild(link);
                link.click();
                link.remove();
                URL.revokeObjectURL(url);
            } catch (error) {
                showResult('Erro de conexão: ' + error.message, 'error');
            }
        }

        document.addEventListener('click', function(e) {
            const filename = e.target.getAttribute('data-download-filename');
            if (filename) {
                downloadFile(filename);
            }
        });

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
                    headers: authHeaders(),
                    body: formData
                });

                if (response.status === 401) {
                    clearSession();
                    showResult('Sessão expirada. Faça login novamente.', 'error');
                    return;
                }

                const result = await response.json();

                if (result.success) {
                    showResult(
                        escapeHtml(result.message) +
                        '<br><br><button class="download-btn" data-download-filename="' + escapeHtml(result.zip_path) + '">⬇️ Download ZIP</button>',
                        'success'
                    );
                    loadFilesList();
                } else {
                    // result.message can echo back ffmpeg's raw output, which
                    // includes the caller-controlled original filename — it
                    // must be escaped before reaching innerHTML below.
                    showResult('Erro: ' + escapeHtml(result.message), 'error');
                }
            } catch (error) {
                showResult('Erro de conexão: ' + error.message, 'error');
            } finally {
                showLoading(false);
            }
        });

        function escapeHtml(str) {
            const div = document.createElement('div');
            div.textContent = str == null ? '' : str;
            return div.innerHTML;
        }

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
                const response = await fetch('/api/status', { headers: authHeaders() });
                if (response.status === 401) {
                    clearSession();
                    document.getElementById('filesList').innerHTML = '<p>Faça login para ver seus arquivos.</p>';
                    return;
                }
                const data = await response.json();

                const filesList = document.getElementById('filesList');

                if (data.files && data.files.length > 0) {
                    filesList.innerHTML = data.files.map(file =>
                        '<div class="file-item">' +
                        '<span>' + escapeHtml(file.filename) + ' (' + formatFileSize(file.size) + ') - ' + escapeHtml(file.created_at) + '</span>' +
                        '<button class="download-btn" data-download-filename="' + escapeHtml(file.filename) + '">⬇️ Download</button>' +
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
        
        // Carregar estado de autenticação e lista de arquivos ao inicializar
        updateAuthUI();
        loadFilesList();
    </script>
</body>
</html>`
}
