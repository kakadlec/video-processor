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
        const data = await response.json();
        if (!data.url) {
            showResult('Erro ao baixar o arquivo.', 'error');
            return;
        }
        const link = document.createElement('a');
        link.href = data.url;
        // Kept for same-origin setups only: the download attribute is
        // ignored for cross-origin URLs, and the attachment behavior comes
        // from the Content-Disposition the storage service returns, which
        // travels inside the URL's signature.
        link.download = filename;
        document.body.appendChild(link);
        link.click();
        link.remove();
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

// Polling pacing for the asynchronous upload flow. POST /upload now answers
// 202 as soon as the job is queued, so the outcome is read from
// GET /api/video-jobs/:id instead of from the upload response.
//
// The interval grows because most jobs finish in the first few polls while a
// long one would otherwise keep asking every two seconds for minutes — and
// every one of those requests is counted by the per-user rate limiter.
const POLL_INITIAL_MS = 2000;
const POLL_BACKOFF_FACTOR = 1.5;
const POLL_MAX_MS = 10000;

function sleep(ms) {
    return new Promise(function(resolve) { setTimeout(resolve, ms); });
}

function setLoadingMessage(message) {
    document.getElementById('loadingMessage').textContent = message;
}

// retryAfterMs reads a Retry-After header expressed in seconds, falling back
// to the caller's current interval when it is absent or unparseable.
function retryAfterMs(response, fallbackMs) {
    const header = response.headers.get('Retry-After');
    const seconds = header === null ? NaN : parseInt(header, 10);
    if (isNaN(seconds) || seconds < 0) {
        return fallbackMs;
    }
    return seconds * 1000;
}

// pollJob follows a job to a terminal status, resolving with the last status
// body it read. It resolves with null when the session is gone or the read
// fails for a reason that will not resolve by waiting.
//
// A 429 is explicitly not a failure: the job is still running, the API is
// only asking for a slower caller. Reporting the job as failed because we
// polled too fast would be a lie about work that is proceeding normally.
async function pollJob(statusUrl) {
    let interval = POLL_INITIAL_MS;
    let wait = POLL_INITIAL_MS;
    for (;;) {
        await sleep(wait);

        let response;
        try {
            response = await fetch(statusUrl, { headers: authHeaders() });
        } catch (error) {
            showResult('Erro de conexão: ' + error.message, 'error');
            return null;
        }

        if (response.status === 401) {
            clearSession();
            showResult('Sessão expirada. Faça login novamente.', 'error');
            return null;
        }
        if (response.status === 429) {
            // Retry-After carries the limiter's own window, which is
            // configurable and routinely larger than POLL_MAX_MS. It is
            // honoured uncapped: the ceiling paces the polling this page
            // chooses to do, it does not license ignoring a delay the API
            // asked for — capping it would just earn another 429. The
            // ordinary interval keeps advancing underneath, so one long
            // wait does not become the cadence.
            interval = Math.min(interval * POLL_BACKOFF_FACTOR, POLL_MAX_MS);
            wait = Math.max(retryAfterMs(response, interval), interval);
            continue;
        }
        if (!response.ok) {
            showResult('Erro ao consultar o status do processamento.', 'error');
            return null;
        }

        const status = await response.json();
        if (status.status === 'completed' || status.status === 'failed') {
            return status;
        }

        if (status.status === 'processing') {
            setLoadingMessage('⏳ Processando vídeo... Isso pode levar alguns minutos.');
        } else {
            setLoadingMessage('⏳ Vídeo na fila, aguardando um processador...');
        }
        interval = Math.min(interval * POLL_BACKOFF_FACTOR, POLL_MAX_MS);
        wait = interval;
    }
}

function showJobOutcome(status) {
    if (status.status === 'failed') {
        showResult('Erro: ' + escapeHtml(status.error_reason || 'o processamento falhou.'), 'error');
        return;
    }
    showResult(
        'Processamento concluído! ' + status.frame_count + ' frames extraídos.' +
        '<br><br><button class="download-btn" data-download-filename="' + escapeHtml(status.storage_key) + '">⬇️ Download ZIP</button>',
        'success'
    );
    loadFilesList();
}

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

    setLoadingMessage('📤 Enviando vídeo...');
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

        // 202 is the accepted path, and it is the same shape whether the
        // submission created a job or matched one already submitted with the
        // same content. Anything else carries the legacy error body.
        if (response.status !== 202) {
            showResult('Erro: ' + escapeHtml(result.message), 'error');
            return;
        }

        setLoadingMessage('⏳ Vídeo na fila, aguardando um processador...');
        const status = await pollJob(result.status_url);
        if (status !== null) {
            showJobOutcome(status);
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
