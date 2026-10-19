axios.defaults.headers.post['Content-Type'] = 'application/x-www-form-urlencoded; charset=UTF-8';
axios.defaults.headers.common['X-Requested-With'] = 'XMLHttpRequest';

axios.interceptors.request.use(
    config => {
        // The existing panel APIs are form-encoded. Diagnostics explicitly
        // requires JSON. Never rewrite a JSON request into "minutes=60":
        // Gin ShouldBindJSON would reject it with invalid character 'm'.
        const headers = config.headers || {};
        const contentType = Object.keys(headers)
            .find(key => key.toLowerCase() === 'content-type');
        if (contentType && /^application\/json\s*(?:;|$)/i.test(String(headers[contentType]))) {
            config.data = JSON.stringify(config.data === undefined ? {} : config.data);
        } else {
            config.data = Qs.stringify(config.data, {
                arrayFormat: 'repeat'
            });
        }
        return config;
    },
    error => Promise.reject(error)
);
