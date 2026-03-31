// Timezone conversion utility for time tooltips
(function () {
  function convertTimeToLocal() {
    // Find all elements with time data
    const timeElements = document.querySelectorAll('[data-utc-time]');

    timeElements.forEach(element => {
      const utcTimeString = element.getAttribute('data-utc-time');
      const displayElement = element.querySelector('.local-time-display');

      if (utcTimeString && displayElement) {
        try {
          // Parse the UTC time
          const utcDate = new Date(utcTimeString);

          // Format date separately
          const localDate = utcDate.toLocaleDateString('en-US', {
            year: 'numeric',
            month: 'long',
            day: 'numeric'
          });

          // Format time separately
          const localTime = utcDate.toLocaleTimeString('en-US', {
            hour: 'numeric',
            minute: '2-digit',
            second: '2-digit',
            hour12: true
          });

          // Get timezone abbreviation
          const tzAbbr = utcDate.toLocaleTimeString('en-US', {
            timeZoneName: 'short'
          }).split(' ').pop();

          // Update the display with stacked format
          displayElement.innerHTML = `${localDate}<br>${localTime} ${tzAbbr}`;
        } catch (error) {
          console.warn('Error converting time:', error);
          // Fallback to original UTC display
        }
      }
    });
  }

  // Convert times when the DOM is ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', convertTimeToLocal);
  } else {
    convertTimeToLocal();
  }

  // Also convert times when new content is loaded via HTMX
  document.addEventListener('htmx:afterSettle', convertTimeToLocal);
})(); 
