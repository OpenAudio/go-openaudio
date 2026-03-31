function themeToggle() {
    return {
        theme: 'light',

        init() {
            console.log('Theme toggle initialized');
            // Read current state from document (set by inline script)
            const isDark = document.documentElement.classList.contains('dark');
            this.theme = isDark ? 'dark' : 'light';
            console.log('Current theme:', this.theme);
        },

        toggleTheme() {
            console.log('Toggle clicked, current theme:', this.theme);
            this.theme = this.theme === 'light' ? 'dark' : 'light';
            console.log('New theme:', this.theme);
            localStorage.setItem('theme', this.theme);
            this.applyTheme();
        },

        applyTheme() {
            console.log('Applying theme:', this.theme);
            if (this.theme === 'dark') {
                document.documentElement.classList.add('dark');
            } else {
                document.documentElement.classList.remove('dark');
            }
        }
    }
}
