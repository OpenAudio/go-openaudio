function searchBar() {
    return {
        query: '',
        showSuggestions: false,
        searchType: '',
        suggestions: [],
        isLoading: false,
        isSelectingSuggestion: false,
        hasNoResults: false,
        audiusApiBase: '',

        init() {
            // Read environment from data attribute and set appropriate endpoint
            const env = this.$el.dataset.env || 'prod';
            switch (env) {
                case 'stage':
                case 'staging':
                    this.audiusApiBase = 'api.staging.audius.co';
                    break;
                case 'dev':
                case 'development':
                    this.audiusApiBase = 'api.audius.co'; // or whatever dev endpoint you use
                    break;
                case 'prod':
                case 'production':
                default:
                    this.audiusApiBase = 'api.audius.co';
                    break;
            }
            console.log('Search component initialized with environment:', env, 'endpoint:', this.audiusApiBase);
        },

        async fetchAllSuggestions() {
            this.isLoading = true;
            try {
                // For empty query, show popular/recent items
                const response = await fetch('/search?q=1');
                const data = await response.json();

                this.searchType = 'All';
                this.suggestions = this.groupSuggestionsByType(data.results || []);
                this.showSuggestions = true;
            } catch (error) {
                console.error('Error fetching suggestions:', error);
                this.suggestions = [];
            } finally {
                this.isLoading = false;
            }
        },

        async handleInput() {
            console.log('Input changed:', this.query);

            // Skip handling if we're in the middle of selecting a suggestion
            if (this.isSelectingSuggestion) {
                return;
            }

            // Reset no results state
            this.hasNoResults = false;

            // Hide suggestions and clear type if query is empty or too short
            if (!this.query.trim()) {
                this.suggestions = [];
                this.showSuggestions = false;
                this.searchType = '';
                return;
            }

            this.isLoading = true;
            try {
                // Make both API calls in parallel
                const [localResponse, audiusResponse] = await Promise.all([
                    fetch(`/search?q=${encodeURIComponent(this.query)}`),
                    fetch(`https://${this.audiusApiBase}/v1/users/search?query=${encodeURIComponent(this.query)}&limit=5`)
                        .catch(error => {
                            console.warn('Audius API error:', error);
                            return null; // Don't fail the entire search if Audius is down
                        })
                ]);

                const localData = await localResponse.json();
                let audiusData = null;

                if (audiusResponse && audiusResponse.ok) {
                    audiusData = await audiusResponse.json();
                }

                if (localData.error) {
                    console.error('Search error:', localData.error);
                    this.suggestions = [];
                    this.showSuggestions = false;
                    this.flashNoResults();
                    return;
                }

                const localResults = localData.results || [];
                const audiusUsers = audiusData?.data || [];

                // Convert Audius users to our suggestion format
                const audiusSuggestions = audiusUsers.map(user => ({
                    id: user.erc_wallet || user.wallet, // Use wallet address as ID for navigation
                    title: user.name,
                    subtitle: `@${user.handle} • ${user.follower_count?.toLocaleString() || 0} followers`,
                    type: 'audius_user',
                    url: `/account/${user.erc_wallet || user.wallet}`,
                    audiusData: {
                        handle: user.handle,
                        bio: user.bio,
                        followerCount: user.follower_count,
                        trackCount: user.track_count,
                        isVerified: user.is_verified,
                        profilePicture: user.profile_picture,
                        wallet: user.erc_wallet || user.wallet
                    }
                })).filter(user => user.id); // Filter out users without wallet addresses

                // Combine results
                const allResults = [...localResults, ...audiusSuggestions];

                // Check if no results found
                if (allResults.length === 0) {
                    this.suggestions = [];
                    this.showSuggestions = false;
                    this.searchType = '';
                    this.flashNoResults();
                    return;
                }

                // Determine search type based on query pattern and results
                if (this.query.startsWith('0x')) {
                    if (this.query.match(/^0x[a-fA-F0-9]{40}$/)) {
                        this.searchType = 'Account';
                    } else if (this.query.match(/^0x[a-fA-F0-9]{64}$/)) {
                        this.searchType = 'Transaction';
                    } else if (this.query.length <= 42) {
                        this.searchType = 'Account';
                    } else {
                        this.searchType = 'Transaction';
                    }
                } else if (this.query.match(/^[0-9]+$/)) {
                    this.searchType = 'Block';
                } else {
                    this.searchType = 'Mixed';
                }

                // Group suggestions by type
                this.suggestions = this.groupSuggestionsByType(allResults);
                this.showSuggestions = true;
            } catch (error) {
                console.error('Error handling input:', error);
                this.suggestions = [];
                this.showSuggestions = false;
                this.flashNoResults();
            } finally {
                this.isLoading = false;
            }
        },

        groupSuggestionsByType(suggestions) {
            const grouped = {};
            suggestions.forEach(suggestion => {
                if (!grouped[suggestion.type]) {
                    grouped[suggestion.type] = [];
                }
                grouped[suggestion.type].push(suggestion);
            });

            // Convert grouped object to array with headers
            const result = [];
            Object.entries(grouped).forEach(([type, items]) => {
                if (items.length > 0) {
                    result.push({
                        id: `header-${type}`,
                        isHeader: true,
                        title: this.formatTypeHeader(type),
                        type: type
                    });
                    result.push(...items);
                }
            });
            return result;
        },

        formatTypeHeader(type) {
            const headers = {
                'track': 'Tracks',
                'username': 'Artists',
                'playlist': 'Playlists',
                'album': 'Albums',
                'block': 'Blocks',
                'account': 'Accounts',
                'transaction': 'Transactions',
                'validator': 'Validators',
                'audius_user': 'Audius Artists'
            };
            return headers[type] || type.charAt(0).toUpperCase() + type.slice(1) + 's';
        },

        getNavigationPath(suggestion) {
            if (suggestion.isHeader) return '';

            // Use the URL from the API response if available
            if (suggestion.url) {
                return suggestion.url;
            }

            // Fallback to original logic
            switch (suggestion.type) {
                case 'block':
                    const blockNum = suggestion.title.match(/#(\d+)/)?.[1] || suggestion.id;
                    return blockNum ? `/block/${blockNum}` : '';
                case 'account':
                    return `/account/${suggestion.id}`;
                case 'transaction':
                    return `/transaction/${suggestion.id}`;
                case 'validator':
                    return `/validator/${suggestion.id}`;
                case 'track':
                    return `/tracks/${suggestion.id}`;
                case 'username':
                    return `/users/${suggestion.title}`;
                case 'playlist':
                    return `/playlists/${suggestion.id}`;
                case 'album':
                    return `/albums/${suggestion.id}`;
                case 'audius_user':
                    return `/account/${suggestion.id}`;
                default:
                    return '';
            }
        },

        selectSuggestion(suggestion) {
            if (suggestion.isHeader) return;

            // Set flag to prevent handleInput from running
            this.isSelectingSuggestion = true;

            const path = this.getNavigationPath(suggestion);
            if (path) {
                // Navigate immediately without setting query
                window.location.href = path;
                return;
            }

            // If for some reason navigation fails, reset the flag
            this.isSelectingSuggestion = false;
            this.showSuggestions = false;
        },

        handleKeydown(event) {
            // Handle Enter key
            if (event.key === 'Enter') {
                event.preventDefault();

                // Find the first non-header suggestion
                const firstSuggestion = this.suggestions.find(s => !s.isHeader);

                if (firstSuggestion) {
                    this.selectSuggestion(firstSuggestion);
                } else if (this.query.trim()) {
                    // If no suggestions but there's a query, flash red
                    this.flashNoResults();
                }
            }
        },

        flashNoResults() {
            this.hasNoResults = true;
            // Reset the flash after 500ms
            setTimeout(() => {
                this.hasNoResults = false;
            }, 500);
        }
    }
}
